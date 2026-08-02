# chat-api Response Header Design

> Version: 2026-08-02  
> Status: approved — implement in `platform/chat-api`  
> Public API: [chat-api.zh-CN.md](../chat-api.zh-CN.md)

## Goal

Allow a custom response header on all chat-api HTTP responses. Clients and gateways use it for sticky routing in multi-replica deployments (e.g. pod name).

## Config

```toml
[projects.platforms.options]
response_header = "X-Custom-Header"
response_header_value = "pod-a"      # fixed value
# OR
response_header_env = "POD_NAME"     # read at startup; ignored when value is set
```

- `response_header` — header name; empty disables the feature
- `response_header_value` — fixed value (wins over env)
- `response_header_env` — env var name; used when `response_header_value` is empty

If header name is set but value resolves to empty, no header is emitted.

## Behavior

1. All HTTP responses include the configured header when name and value are both non-empty.
2. CORS: header name is added to `Access-Control-Allow-Headers` and `Access-Control-Expose-Headers`.

No SSE body changes. No server-side affinity validation — routing is handled by the gateway/client.

## Example (Kubernetes)

```yaml
env:
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
```

```toml
response_header = "X-Custom-Header"
response_header_env = "POD_NAME"
```

Gateway reads `X-Custom-Header` from the first response and routes subsequent cancel/interaction requests to the same pod.
