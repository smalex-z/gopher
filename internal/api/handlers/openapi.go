package handlers

import (
	"net/http"
)

const openAPISpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Gopher External API",
    "description": "REST API for programmatic tunnel management. Machines and tunnels are separate resources — bootstrap the machine first, wait for it to connect, then create tunnels on it.",
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
      "Machine": {
        "type": "object",
        "properties": {
          "id":            { "type": "string",  "example": "a1b2c3d4e5f6a7b8" },
          "status":        { "type": "string",  "enum": ["pending", "connected", "failed"] },
          "public_ssh":    { "type": "boolean", "description": "Whether the SSH back-tunnel is publicly reachable" },
          "bootstrap_url": { "type": "string",  "description": "One-time bootstrap URL (present on creation response only)" },
          "error":         { "type": "string",  "description": "Failure reason (present when failed)" },
          "created_at":    { "type": "string",  "format": "date-time" }
        }
      },
      "Tunnel": {
        "type": "object",
        "properties": {
          "id":          { "type": "string",  "example": "f8e7d6c5b4a39281" },
          "machine_id":  { "type": "string",  "example": "a1b2c3d4e5f6a7b8" },
          "status":      { "type": "string",  "enum": ["active", "failed"] },
          "subdomain":   { "type": "string",  "example": "my-server-a1b2c3" },
          "target_ip":   { "type": "string",  "example": "127.0.0.1" },
          "target_port": { "type": "integer", "example": 3000 },
          "tunnel_url":  { "type": "string",  "example": "https://my-server-a1b2c3.example.com", "description": "Public URL (present when active)" },
          "error":       { "type": "string",  "description": "Failure reason (present when failed)" },
          "created_at":  { "type": "string",  "format": "date-time" }
        }
      },
      "PaginatedMachines": {
        "type": "object",
        "properties": {
          "items":  { "type": "array", "items": { "$ref": "#/components/schemas/Machine" } },
          "total":  { "type": "integer" },
          "limit":  { "type": "integer" },
          "offset": { "type": "integer" }
        }
      },
      "PaginatedTunnels": {
        "type": "object",
        "properties": {
          "items":  { "type": "array", "items": { "$ref": "#/components/schemas/Tunnel" } },
          "total":  { "type": "integer" },
          "limit":  { "type": "integer" },
          "offset": { "type": "integer" }
        }
      },
      "Error": {
        "type": "object",
        "properties": {
          "success": { "type": "boolean", "example": false },
          "error":   { "type": "string",  "example": "machine is not connected (status: pending)" }
        }
      }
    },
    "parameters": {
      "limit":  { "name": "limit",  "in": "query", "schema": { "type": "integer", "default": 50, "maximum": 200 } },
      "offset": { "name": "offset", "in": "query", "schema": { "type": "integer", "default": 0 } },
      "machineId": { "name": "id", "in": "path", "required": true, "schema": { "type": "string" } },
      "tunnelId":  { "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }
    },
    "responses": {
      "NoContent":    { "description": "Success, no body" },
      "Unauthorized": { "description": "Missing or invalid API key" },
      "NotFound":     { "description": "Resource not found" },
      "Conflict":     { "description": "Subdomain already in use" },
      "Unavailable":  { "description": "External API not configured — generate a key in Access Control" }
    }
  },
  "paths": {
    "/machines": {
      "get": {
        "summary": "List machines",
        "operationId": "listMachines",
        "parameters": [
          { "$ref": "#/components/parameters/limit" },
          { "$ref": "#/components/parameters/offset" }
        ],
        "responses": {
          "200": {
            "description": "Paginated list of machines",
            "content": { "application/json": { "schema": { "properties": { "success": { "type": "boolean" }, "data": { "$ref": "#/components/schemas/PaginatedMachines" } } } } }
          },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "503": { "$ref": "#/components/responses/Unavailable" }
        }
      },
      "post": {
        "summary": "Bootstrap a machine",
        "operationId": "createMachine",
        "description": "Generates a one-time bootstrap token. Run the returned bootstrap_url on the target VM, then poll GET /machines/:id until status is 'connected'.",
        "requestBody": {
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": {
                  "public_ssh": { "type": "boolean", "default": false, "description": "Expose SSH back-tunnel publicly" },
                  "ssh_key_id": { "type": "string", "description": "SSH key ID to install (defaults to server default)" }
                }
              }
            }
          }
        },
        "responses": {
          "201": {
            "description": "Machine record created",
            "content": { "application/json": { "schema": { "properties": { "success": { "type": "boolean" }, "data": { "$ref": "#/components/schemas/Machine" } } } } }
          },
          "400": { "description": "Invalid request or SSH key not found" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "503": { "$ref": "#/components/responses/Unavailable" }
        }
      }
    },
    "/machines/{id}": {
      "parameters": [ { "$ref": "#/components/parameters/machineId" } ],
      "get": {
        "summary": "Get a machine",
        "operationId": "getMachine",
        "description": "Poll after bootstrap until status transitions to 'connected' or 'failed'.",
        "responses": {
          "200": {
            "description": "Machine record",
            "content": { "application/json": { "schema": { "properties": { "success": { "type": "boolean" }, "data": { "$ref": "#/components/schemas/Machine" } } } } }
          },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      },
      "delete": {
        "summary": "Delete a machine",
        "operationId": "deleteMachine",
        "description": "Full teardown: removes rathole client from the VM (best-effort SSH), removes all tunnels and Caddy routes, and deletes all database records. Safe to call after the VM is destroyed.",
        "responses": {
          "204": { "$ref": "#/components/responses/NoContent" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      }
    },
    "/tunnels": {
      "get": {
        "summary": "List tunnels",
        "operationId": "listTunnels",
        "parameters": [
          { "$ref": "#/components/parameters/limit" },
          { "$ref": "#/components/parameters/offset" }
        ],
        "responses": {
          "200": {
            "description": "Paginated list of tunnels",
            "content": { "application/json": { "schema": { "properties": { "success": { "type": "boolean" }, "data": { "$ref": "#/components/schemas/PaginatedTunnels" } } } } }
          },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "503": { "$ref": "#/components/responses/Unavailable" }
        }
      },
      "post": {
        "summary": "Create a tunnel",
        "operationId": "createTunnel",
        "description": "Creates a service tunnel on an already-connected machine. Synchronous — the tunnel is live or failed by the time this call returns.",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "required": ["machine_id", "target_port"],
                "properties": {
                  "machine_id":  { "type": "string",  "description": "ID of a connected machine" },
                  "target_port": { "type": "integer", "example": 3000 },
                  "subdomain":   { "type": "string",  "description": "Auto-generated from machine name if omitted" },
                  "target_ip":   { "type": "string",  "default": "127.0.0.1" },
                  "private":     { "type": "boolean", "default": false, "description": "Bind to 127.0.0.1 on the VPS (not publicly reachable)" },
                  "no_tls":      { "type": "boolean", "default": false, "description": "Skip Caddy TLS; serve plain http://" }
                }
              }
            }
          }
        },
        "responses": {
          "201": {
            "description": "Tunnel created and active",
            "content": { "application/json": { "schema": { "properties": { "success": { "type": "boolean" }, "data": { "$ref": "#/components/schemas/Tunnel" } } } } }
          },
          "400": { "description": "Invalid request or machine not connected" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" },
          "409": { "$ref": "#/components/responses/Conflict" },
          "503": { "$ref": "#/components/responses/Unavailable" }
        }
      }
    },
    "/tunnels/{id}": {
      "parameters": [ { "$ref": "#/components/parameters/tunnelId" } ],
      "get": {
        "summary": "Get a tunnel",
        "operationId": "getTunnel",
        "responses": {
          "200": {
            "description": "Tunnel record",
            "content": { "application/json": { "schema": { "properties": { "success": { "type": "boolean" }, "data": { "$ref": "#/components/schemas/Tunnel" } } } } }
          },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      },
      "delete": {
        "summary": "Delete a tunnel",
        "operationId": "deleteTunnel",
        "description": "Removes this service tunnel only. The machine remains and can have new tunnels created on it. To teardown everything, use DELETE /machines/:id.",
        "responses": {
          "204": { "$ref": "#/components/responses/NoContent" },
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
