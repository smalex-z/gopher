package handlers

import (
	"net/http"
)

const openAPISpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Gopher External API",
    "description": "REST API for programmatic tunnel management. All endpoints require Bearer token authentication.",
    "version": "1.0.0"
  },
  "servers": [
    { "url": "/api/v1", "description": "Current server" }
  ],
  "security": [
    { "bearerAuth": [] }
  ],
  "components": {
    "securitySchemes": {
      "bearerAuth": {
        "type": "http",
        "scheme": "bearer",
        "description": "API key from Access Control page or GOPHER_API_KEY env var"
      }
    },
    "schemas": {
      "Tunnel": {
        "type": "object",
        "properties": {
          "id":            { "type": "string",  "example": "a1b2c3d4e5f6a7b8" },
          "status":        { "type": "string",  "enum": ["pending", "active", "failed"] },
          "subdomain":     { "type": "string",  "example": "myapp" },
          "target_ip":     { "type": "string",  "example": "127.0.0.1" },
          "bootstrap_url": { "type": "string",  "description": "One-time bootstrap URL (pending only)" },
          "tunnel_url":    { "type": "string",  "example": "https://myapp.example.com", "description": "Public URL (active only)" },
          "error":         { "type": "string",  "description": "Failure reason (failed only)" },
          "created_at":    { "type": "string",  "format": "date-time" }
        }
      },
      "Error": {
        "type": "object",
        "properties": {
          "success": { "type": "boolean", "example": false },
          "error":   { "type": "string",  "example": "subdomain already in use" }
        }
      }
    },
    "responses": {
      "Unauthorized":   { "description": "Missing or invalid API key" },
      "NotFound":       { "description": "Tunnel not found" },
      "Conflict":       { "description": "Subdomain already in use" },
      "Unavailable":    { "description": "External API not configured — generate a key in Access Control" }
    }
  },
  "paths": {
    "/tunnels": {
      "get": {
        "summary": "List tunnels",
        "operationId": "listTunnels",
        "parameters": [
          { "name": "limit",  "in": "query", "schema": { "type": "integer", "default": 50,  "maximum": 200 } },
          { "name": "offset", "in": "query", "schema": { "type": "integer", "default": 0 } }
        ],
        "responses": {
          "200": {
            "description": "Paginated list of tunnels",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "success": { "type": "boolean" },
                    "data": {
                      "type": "object",
                      "properties": {
                        "items":  { "type": "array", "items": { "$ref": "#/components/schemas/Tunnel" } },
                        "total":  { "type": "integer" },
                        "limit":  { "type": "integer" },
                        "offset": { "type": "integer" }
                      }
                    }
                  }
                }
              }
            }
          },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "503": { "$ref": "#/components/responses/Unavailable" }
        }
      },
      "post": {
        "summary": "Create a tunnel",
        "operationId": "createTunnel",
        "description": "Provisions a new tunnel asynchronously. Run the returned bootstrap_url on the target VM to activate it.",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "required": ["subdomain", "target_port"],
                "properties": {
                  "subdomain":   { "type": "string",  "example": "myapp" },
                  "target_ip":   { "type": "string",  "example": "127.0.0.1", "description": "Defaults to 127.0.0.1" },
                  "target_port": { "type": "integer", "example": 3000 }
                }
              }
            }
          }
        },
        "responses": {
          "201": {
            "description": "Tunnel created (pending activation)",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "success": { "type": "boolean" },
                    "data":    { "$ref": "#/components/schemas/Tunnel" }
                  }
                }
              }
            }
          },
          "400": { "description": "Invalid request" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "409": { "$ref": "#/components/responses/Conflict" },
          "503": { "$ref": "#/components/responses/Unavailable" }
        }
      }
    },
    "/tunnels/{id}": {
      "parameters": [
        { "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }
      ],
      "get": {
        "summary": "Get a tunnel",
        "operationId": "getTunnel",
        "description": "Poll this endpoint after creation to check when status transitions to active or failed.",
        "responses": {
          "200": {
            "description": "Tunnel record",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "success": { "type": "boolean" },
                    "data":    { "$ref": "#/components/schemas/Tunnel" }
                  }
                }
              }
            }
          },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      },
      "delete": {
        "summary": "Delete a tunnel",
        "operationId": "deleteTunnel",
        "description": "Full teardown: removes rathole client from the VM (best-effort SSH), Caddy route, and all database records. Safe to call after the VM is destroyed.",
        "responses": {
          "204": { "description": "Deleted successfully" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      }
    }
  }
}`

func ServeOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(openAPISpec))
}
