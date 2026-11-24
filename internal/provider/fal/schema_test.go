package fal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-media-gateway/internal/provider"
)

// openapiFixture mirrors the shape fal actually serves, including an enum
// nested inside anyOf (a nullable enum) and a required list.
const openapiFixture = `{
  "components": {
    "schemas": {
      "ImageSize": {
        "type": "object",
        "properties": {"width": {"type": "integer"}, "height": {"type": "integer"}}
      },
      "SomethingInput": {
        "type": "object",
        "required": ["prompt", "image_urls"],
        "properties": {
          "prompt": {"type": "string"},
          "image_urls": {"type": "array", "maxItems": 16},
          "mask_url": {"anyOf": [{"type": "string"}, {"type": "null"}]},
          "num_images": {"type": "integer", "default": 1, "maximum": 10, "minimum": 1},
          "quality": {"type": "string", "default": "high", "enum": ["auto", "low", "high"]},
          "output_format": {"type": "string", "default": "png", "enum": ["png", "svg"]},
          "image_size": {
            "default": "auto",
            "anyOf": [
              {"$ref": "#/components/schemas/ImageSize"},
              {"enum": ["square_hd", "square", "auto"]}
            ]
          }
        }
      }
    }
  }
}`

func schemaServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(openapiFixture))
	}))
	t.Cleanup(srv.Close)
	restore := SetOpenAPIBase(srv.URL)
	t.Cleanup(restore)
	_ = gotPath
	return srv, &gotQuery
}

// TestEndpointSchemaParses makes sure the OpenAPI document is reduced to the
// fields the gateway needs: enum (including the anyOf-nested one), maximum,
// maxItems, required and default.
func TestEndpointSchemaParses(t *testing.T) {
	srv, gotQuery := schemaServer(t)

	call := provider.Call{HTTP: srv.Client()}
	schema, err := Adapter{}.EndpointSchema(context.Background(), call, "some/model/edit")
	if err != nil {
		t.Fatal(err)
	}
	if *gotQuery != "endpoint_id=some%2Fmodel%2Fedit" {
		t.Fatalf("query: %q", *gotQuery)
	}

	if f, ok := schema.Field("num_images"); !ok {
		t.Fatal("num_images missing")
	} else if f.Maximum == nil || *f.Maximum != 10 {
		t.Fatalf("num_images maximum: %+v", f)
	}

	if f, ok := schema.Field("image_urls"); !ok {
		t.Fatal("image_urls missing")
	} else if f.MaxItems == nil || *f.MaxItems != 16 || !f.Required {
		t.Fatalf("image_urls: %+v", f)
	}

	// Enum nested in anyOf must still be found.
	if f, ok := schema.Field("image_size"); !ok {
		t.Fatal("image_size missing")
	} else if len(f.Enum) != 3 || f.Enum[0] != "square_hd" {
		t.Fatalf("image_size enum: %+v", f.Enum)
	}

	if f, ok := schema.Field("quality"); !ok {
		t.Fatal("quality missing")
	} else if f.Default != "high" {
		t.Fatalf("quality default: %+v", f.Default)
	}

	// Only *Input properties are reported; the ImageSize helper schema is not
	// an input and must not leak into the field list.
	if _, ok := schema.Field("width"); ok {
		t.Fatal("helper schema leaked into fields")
	}
	if len(schema.Fields) != 7 {
		t.Fatalf("field count: %d (%v)", len(schema.Fields), schema.Fields)
	}
}

// TestEndpointSchemaHTTPError keeps a bad endpoint from looking like an empty
// schema.
func TestEndpointSchemaHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"detail":"not found"}`))
	}))
	defer srv.Close()
	restore := SetOpenAPIBase(srv.URL)
	defer restore()

	call := provider.Call{HTTP: srv.Client()}
	if _, err := (Adapter{}).EndpointSchema(context.Background(), call, "nope"); err == nil {
		t.Fatal("expected an error for 404")
	}
}

// TestEndpointSchemaNoInputSchema guards against a document that parses but
// carries no *Input schema.
func TestEndpointSchemaNoInputSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"components":{"schemas":{"Other":{"properties":{}}}}}`))
	}))
	defer srv.Close()
	restore := SetOpenAPIBase(srv.URL)
	defer restore()

	call := provider.Call{HTTP: srv.Client()}
	if _, err := (Adapter{}).EndpointSchema(context.Background(), call, "x"); err == nil {
		t.Fatal("expected an error when no *Input schema exists")
	}
}

// TestEndpointSchemaSendsKey confirms the optional Authorization header, which
// fal accepts to raise rate limits.
func TestEndpointSchemaSendsKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(openapiFixture))
	}))
	defer srv.Close()
	restore := SetOpenAPIBase(srv.URL)
	defer restore()

	call := provider.Call{HTTP: srv.Client(), Account: provider.AccountConfig{APIKey: "k1"}}
	if _, err := (Adapter{}).EndpointSchema(context.Background(), call, "x"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Key k1" {
		t.Fatalf("auth header: %q", gotAuth)
	}
}
