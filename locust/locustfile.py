"""
Load test for the Product API.

Request mix: 90% GET, 10% POST (task weights 9:1). In a real store, people
browse products far more often than products are added or updated, so
reads should dominate the test.

Each simulated user first creates a few products (on_start), then mostly
reads products it knows exist, so GETs return 200 instead of 404.

Run ONE user class at a time so the comparison is fair:
    locust -f locust/locustfile.py ProductHttpUser --host http://<IP>:8080
    locust -f locust/locustfile.py ProductFastUser --host http://<IP>:8080
"""
import random

from locust import FastHttpUser, HttpUser, TaskSet, between, task


def make_product(pid):
    return {
        "product_id": pid,
        "sku": f"SKU-{pid}",
        "manufacturer": "Acme",
        "category_id": random.randint(1, 50),
        "weight": random.randint(0, 5000),
        "some_other_id": random.randint(1, 1000),
    }


class ProductTasks(TaskSet):
    """Shared behavior, so both user classes send identical traffic."""

    def on_start(self):
        self.ids = []
        for _ in range(5):
            self.create_product()

    def create_product(self):
        pid = random.randint(1, 1_000_000)
        # name= groups every ID under one row in Locust's statistics,
        # instead of one row per product ID.
        with self.client.post(
            f"/products/{pid}/details",
            json=make_product(pid),
            name="POST /products/{id}/details",
            catch_response=True,
        ) as resp:
            if resp.status_code == 204:
                self.ids.append(pid)
                resp.success()
            else:
                resp.failure(f"unexpected status {resp.status_code}")

    @task(9)
    def get_product(self):
        if not self.ids:
            self.create_product()
            return
        pid = random.choice(self.ids)
        self.client.get(f"/products/{pid}", name="GET /products/{id}")

    @task(1)
    def add_product(self):
        self.create_product()


class ProductHttpUser(HttpUser):
    """Uses python-requests: easy and standard, but more CPU per request."""
    tasks = [ProductTasks]
    wait_time = between(0.1, 0.5)


class ProductFastUser(FastHttpUser):
    """Uses geventhttpclient: much less CPU per request on the Locust side."""
    tasks = [ProductTasks]
    wait_time = between(0.1, 0.5)
