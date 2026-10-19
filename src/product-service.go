// product-service.go
//
// Implements the two Product endpoints from api.yaml using Gin:
//
//	GET  /products/{productId}          (operationId: getProduct)
//	POST /products/{productId}/details  (operationId: addProductDetails)
//
// Products are kept in memory, so all data is lost when the server stops.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ===================== Observability: RED metrics =====================

// Labels use the route template (e.g. /products/:productId), not the raw
// path, so every product ID shares one time series instead of creating one
// per ID (which would blow up Prometheus's memory, "high cardinality").
var (
	// Rate: total requests, counted per method, route, and status code.
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests handled.",
	}, []string{"method", "route", "status"})

	// Errors: requests that returned a non-2xx status code.
	errorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_request_errors_total",
		Help: "Total HTTP requests that returned a non-2xx status.",
	}, []string{"method", "route", "status"})

	// Duration: latency histogram. Buckets range from 0.1 ms to ~3.3 s,
	// because this in-memory server often answers in well under 1 ms.
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: prometheus.ExponentialBuckets(0.0001, 2, 16),
	}, []string{"method", "route"})
)

// observability runs around every request: it times the request, then
// records the RED metrics and writes one structured JSON log line.
func observability(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next() // run the rest of the chain (recovery + the handler)

		route := c.FullPath() // route template; "" if no route matched
		if route == "" {
			route = "unmatched"
		}
		if route == "/metrics" {
			return // don't measure Prometheus's own scrapes
		}

		status := c.Writer.Status()
		statusLabel := strconv.Itoa(status)
		duration := time.Since(start)

		requestsTotal.WithLabelValues(c.Request.Method, route, statusLabel).Inc()
		if status < 200 || status >= 300 {
			errorsTotal.WithLabelValues(c.Request.Method, route, statusLabel).Inc()
		}
		requestDuration.WithLabelValues(c.Request.Method, route).Observe(duration.Seconds())

		logger.Info("request",
			"method", c.Request.Method,
			"route", route,
			"path", c.Request.URL.Path,
			"status", status,
			"duration_ms", float64(duration.Microseconds())/1000.0,
		)
	}
}

// ===================== Data types (components/schemas) =====================

// Product mirrors the Product schema. The json tags tell Go to use the
// snake_case names from the spec when converting to and from JSON.
type Product struct {
	ProductID    int32  `json:"product_id"`
	SKU          string `json:"sku"`
	Manufacturer string `json:"manufacturer"`
	CategoryID   int32  `json:"category_id"`
	Weight       int32  `json:"weight"`
	SomeOtherID  int32  `json:"some_other_id"`
}

// productInput is used only to read POST request bodies. Its fields are
// pointers so we can tell "field missing" (nil) apart from "field sent as 0".
// That matters because weight is required but is allowed to be 0.
type productInput struct {
	ProductID    *int32  `json:"product_id"`
	SKU          *string `json:"sku"`
	Manufacturer *string `json:"manufacturer"`
	CategoryID   *int32  `json:"category_id"`
	Weight       *int32  `json:"weight"`
	SomeOtherID  *int32  `json:"some_other_id"`
}

// ErrorResponse mirrors the Error schema. "details" is optional in the
// spec, so omitempty leaves it out of the JSON when it is empty.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// ===================== In-memory storage =====================

// ProductStore keeps products in a map keyed by product ID.
// Gin handles requests concurrently, so a read-write mutex protects the map:
// many readers can hold it at once, but a writer gets exclusive access.
type ProductStore struct {
	mu       sync.RWMutex
	products map[int32]Product
}

func NewProductStore() *ProductStore {
	return &ProductStore{products: make(map[int32]Product)}
}

// Get returns the product and true if it exists, or false if it doesn't.
func (s *ProductStore) Get(id int32) (Product, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.products[id]
	return p, ok
}

// Save adds a new product or replaces an existing one with the same ID.
func (s *ProductStore) Save(p Product) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.products[p.ProductID] = p
}

// ===================== Helpers =====================

// respondError sends a JSON body that matches the Error schema.
func respondError(c *gin.Context, status int, code, message, details string) {
	c.JSON(status, ErrorResponse{Error: code, Message: message, Details: details})
}

// parseProductID reads {productId} from the URL and applies the spec's
// rules for it: an integer that fits in int32, with a minimum of 1.
func parseProductID(c *gin.Context) (int32, bool) {
	id, err := strconv.ParseInt(c.Param("productId"), 10, 32)
	if err != nil || id < 1 {
		return 0, false
	}
	return int32(id), true
}

// validate checks a request body against the Product schema's rules
// (required fields, minimums, string lengths). It returns a description
// of the first problem it finds, or "" if the body is valid.
func (in productInput) validate() string {
	// Every field is listed under "required" in the schema.
	switch {
	case in.ProductID == nil:
		return "product_id is required"
	case in.SKU == nil:
		return "sku is required"
	case in.Manufacturer == nil:
		return "manufacturer is required"
	case in.CategoryID == nil:
		return "category_id is required"
	case in.Weight == nil:
		return "weight is required"
	case in.SomeOtherID == nil:
		return "some_other_id is required"
	}

	// Per-field rules. Type and int32-range errors are already caught
	// earlier, when the JSON is decoded into the struct.
	if *in.ProductID < 1 {
		return "product_id must be at least 1"
	}
	if n := utf8.RuneCountInString(*in.SKU); n < 1 || n > 100 {
		return "sku must be between 1 and 100 characters"
	}
	if n := utf8.RuneCountInString(*in.Manufacturer); n < 1 || n > 200 {
		return "manufacturer must be between 1 and 200 characters"
	}
	if *in.CategoryID < 1 {
		return "category_id must be at least 1"
	}
	if *in.Weight < 0 {
		return "weight must be at least 0"
	}
	if *in.SomeOtherID < 1 {
		return "some_other_id must be at least 1"
	}
	return ""
}

// ===================== Handlers =====================

// ProductHandler connects the HTTP endpoints to the store.
type ProductHandler struct {
	store *ProductStore
}

// GetProduct handles GET /products/{productId}.
// Responses: 200 with the Product, 404 if not found (500 via recovery).
func (h *ProductHandler) GetProduct(c *gin.Context) {
	id, ok := parseProductID(c)
	if !ok {
		// The spec lists no 400 for this endpoint, so an invalid ID is
		// reported as 404: no product can ever have that ID.
		respondError(c, http.StatusNotFound, "PRODUCT_NOT_FOUND",
			"Product not found", "Product ID must be a positive 32-bit integer")
		return
	}

	product, found := h.store.Get(id)
	if !found {
		respondError(c, http.StatusNotFound, "PRODUCT_NOT_FOUND",
			"Product not found", fmt.Sprintf("No product exists with ID %d", id))
		return
	}

	c.JSON(http.StatusOK, product)
}

// AddProductDetails handles POST /products/{productId}/details.
// Responses: 204 on success (no body), 400 for invalid input (500 via recovery).
func (h *ProductHandler) AddProductDetails(c *gin.Context) {
	id, ok := parseProductID(c)
	if !ok {
		respondError(c, http.StatusBadRequest, "INVALID_INPUT",
			"The provided input data is invalid", "Product ID must be a positive 32-bit integer")
		return
	}

	// Decode the JSON body. This fails on empty or malformed JSON, wrong
	// types (e.g. a string for weight), or numbers too big for int32.
	var in productInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_INPUT",
			"Request body must be valid JSON matching the Product schema", err.Error())
		return
	}

	if problem := in.validate(); problem != "" {
		respondError(c, http.StatusBadRequest, "INVALID_INPUT",
			"The provided input data is invalid", problem)
		return
	}

	// The ID in the URL and the ID in the body must refer to the same product.
	if *in.ProductID != id {
		respondError(c, http.StatusBadRequest, "INVALID_INPUT",
			"The provided input data is invalid",
			fmt.Sprintf("product_id in body (%d) does not match productId in URL (%d)", *in.ProductID, id))
		return
	}

	// "Add or update": store the product, replacing any existing one.
	h.store.Save(Product{
		ProductID:    *in.ProductID,
		SKU:          *in.SKU,
		Manufacturer: *in.Manufacturer,
		CategoryID:   *in.CategoryID,
		Weight:       *in.Weight,
		SomeOtherID:  *in.SomeOtherID,
	})

	// 204 No Content: success, with no response body.
	c.Status(http.StatusNoContent)
}

// ===================== Server setup =====================

func main() {
	// Structured JSON logs to stdout (one JSON object per line).
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	handler := &ProductHandler{store: NewProductStore()}

	router := gin.New()

	// Registered first so it wraps everything, including recovery:
	// a request that panics is still measured and logged as a 500.
	router.Use(observability(logger))

	// If any handler panics, return a 500 with an Error-schema body
	// instead of crashing the server.
	router.Use(gin.CustomRecovery(func(c *gin.Context, recovered any) {
		c.AbortWithStatusJSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "INTERNAL_ERROR",
			Message: "Internal server error",
		})
	}))

	// Route each endpoint to its handler. Gin writes path parameters as
	// :productId where OpenAPI writes {productId}.
	router.GET("/products/:productId", handler.GetProduct)
	router.POST("/products/:productId/details", handler.AddProductDetails)

	// Prometheus scrapes this endpoint to collect the metrics above.
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// ":8080" listens on all network interfaces, which Docker needs.
	logger.Info("Product service listening", "addr", ":8080")
	if err := router.Run(":8080"); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
