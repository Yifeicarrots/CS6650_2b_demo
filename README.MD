# Product API on AWS ECS (Terraform)

This repo deploys the **Product API** (a Go + Gin server) to **AWS ECS Fargate** using Terraform. One `terraform apply` builds the Docker image, pushes it to ECR, and creates the networking, logging, and ECS service needed to run it. No clicking through the AWS console required.

## Repository structure

```
.
├── src/                      # The Product API server
│   ├── product-service.go    #   Gin server: GET and POST Product endpoints
│   ├── go.mod / go.sum       #   Go dependencies
│   └── Dockerfile            #   Multi-stage build of the server image
└── terraform/                # Infrastructure as code
    ├── provider.tf           #   AWS and Docker providers, ECR login
    ├── variables.tf          #   Settings: region, names, port, task count
    ├── main.tf               #   Wires the modules together, builds and pushes the image
    ├── outputs.tf            #   Prints the ECS cluster and service names
    └── modules/
        ├── network/          #   Subnets and security group (opens the container port)
        ├── ecr/              #   Image repository
        ├── logging/          #   CloudWatch log group
        └── ecs/              #   Cluster, task definition, and service
```

## Prerequisites

Install these on your machine:

- **Docker Desktop**, and make sure it is **running** before you deploy (Terraform builds the image locally).
- **Terraform** (a recent version)
- **AWS CLI v2**
- **AWS Learner's Lab** access. The Terraform code reuses the lab's pre-created `LabRole` IAM role. On a regular AWS account, you would need to provide a role with ECS task-execution permissions instead.

Check your installs with `docker --version`, `terraform -version`, and `aws --version`.

## 1. Clone the repo

```bash
git clone https://github.com/Yifeicarrots/CS6650_2b_demo.git
cd CS6650_2b_demo
```

## 2. Set up AWS credentials

Start your Learner's Lab session, open **AWS Details**, and click **Show** next to *AWS CLI* to see your temporary credentials. Then run:

```bash
aws configure
```

Enter the access key ID and secret access key from the lab, use `us-west-2` as the region, and `json` as the output format. Then add the session token:

```bash
aws configure set aws_session_token <YOUR-SESSION-TOKEN>
```

Confirm it works:

```bash
aws sts get-caller-identity
```

> **Note:** Learner's Lab credentials are temporary. Each time your lab session restarts, repeat this step with the new credentials.

## 3. Deploy

```bash
cd terraform
terraform init
terraform apply -auto-approve
```

`terraform init` downloads the AWS and Docker providers. `terraform apply` then builds the image from `src/`, pushes it to ECR, and creates the network, log group, and ECS service. This takes a few minutes. When it finishes, it prints the cluster and service names.

## 4. Find your server's public IP

ECS gives the running container a public IP address. Wait about a minute after `apply` finishes for the task to start, then run these from the `terraform/` folder:

```bash
CLUSTER=$(terraform output -raw ecs_cluster_name)
SERVICE=$(terraform output -raw ecs_service_name)

TASK=$(aws ecs list-tasks --cluster "$CLUSTER" --service-name "$SERVICE" \
  --query 'taskArns[0]' --output text)

ENI=$(aws ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" \
  --query "tasks[0].attachments[0].details[?name=='networkInterfaceId'].value" --output text)

IP=$(aws ec2 describe-network-interfaces --network-interface-ids "$ENI" \
  --query 'NetworkInterfaces[0].Association.PublicIp' --output text)

echo $IP
```

The IP changes whenever the task restarts or is redeployed, so rerun these commands if requests stop working.

## 5. Send requests

The server listens on port **8080**.

| Method | Path | Description | Responses |
|---|---|---|---|
| `GET` | `/products/{productId}` | Get a product by ID | `200`, `404`, `500` |
| `POST` | `/products/{productId}/details` | Add or update a product | `204`, `400`, `404`, `500` |

A product has six required fields: `product_id` (≥ 1, must match the ID in the URL), `sku` (1–100 characters), `manufacturer` (1–200 characters), `category_id` (≥ 1), `weight` (≥ 0, in grams), and `some_other_id` (≥ 1). Errors return JSON with `error`, `message`, and `details` fields.

The store starts empty and is kept in memory, so add a product first:

```bash
# Add a product → 204 No Content
curl -i -X POST http://$IP:8080/products/1/details \
  -H "Content-Type: application/json" \
  -d '{"product_id":1,"sku":"ABC-123","manufacturer":"Acme","category_id":2,"weight":500,"some_other_id":3}'

# Get it back → 200 OK with the product JSON
curl -i http://$IP:8080/products/1

# Product that does not exist → 404 Not Found
curl -i http://$IP:8080/products/999

# Invalid body (missing weight) → 400 Bad Request
curl -i -X POST http://$IP:8080/products/2/details \
  -H "Content-Type: application/json" \
  -d '{"product_id":2,"sku":"ABC-123","manufacturer":"Acme","category_id":2,"some_other_id":3}'
```

> **Note:** Data lives in the container's memory, so it is lost whenever the task restarts or is redeployed.

## 6. Redeploy after changing the code

After editing files in `src/`, run from `terraform/`:

```bash
terraform apply -auto-approve
aws ecs update-service --cluster "$CLUSTER" --service "$SERVICE" --force-new-deployment
```

The first command rebuilds and pushes the image. The second is needed because the image is always tagged `latest`, so ECS doesn't notice the change on its own; forcing a new deployment makes it pull the new image. Then repeat step 4, since the IP will change.

## 7. Clean up

When you're done, delete everything to save your lab credits:

```bash
terraform destroy -auto-approve
```

## Troubleshooting

- **`Cannot connect to the Docker daemon`**: Docker Desktop isn't running. Start it and retry.
- **`Error pinging Docker server: Cannot connect to the Docker daemon at unix:///var/run/docker.sock`** (on a Mac, even with Docker Desktop running): Terraform's Docker provider looks for Docker at the standard Linux location, but Docker Desktop uses a different one. Run `export DOCKER_HOST=unix://$HOME/.docker/run/docker.sock` in the same terminal before `terraform apply`, or enable **Settings → Advanced → "Allow the default Docker socket to be used"** in Docker Desktop.
- **`ExpiredToken` or other credential errors**: your lab session's credentials have expired. Repeat step 2 with fresh ones.
- **Requests hang or time out**: the task may still be starting, or it restarted with a new IP. Rerun step 4. You can also check the task's status under ECS in the AWS console, and its logs in **CloudWatch → Log groups → `/ecs/CS6650L2`**.
- **No task is running a few minutes after the first `apply`**: Terraform creates the ECS service and pushes the image in parallel, so the first task can start before the image exists in ECR. ECS normally retries on its own; if it doesn't, run the `aws ecs update-service ... --force-new-deployment` command from step 6.
- **Task keeps stopping with `exec format error` in the logs**: the image was built for a different CPU architecture than the one ECS runs it on. ECS here runs on x86 (`amd64`), so `src/Dockerfile` cross-compiles the server with `GOARCH=amd64` and uses an `amd64` base image. This lets the image work even when built on an Apple Silicon (ARM) Mac. Keep both settings if you change the Dockerfile.
- **`quote>` prompt after pasting commands** (zsh on Mac): zsh doesn't treat `#` as a comment by default when pasting, so an apostrophe in a comment line opens a quote. Press Ctrl+C, then paste commands without the comment lines, or run `setopt interactivecomments` first.