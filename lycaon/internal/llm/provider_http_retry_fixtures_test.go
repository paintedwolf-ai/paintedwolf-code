package llm

// MinimalShipHTTPRetryYAML is an indented http_retry block for synthetic ship
// fixtures. Production providers.yaml must declare full per-kind policy.
const MinimalShipHTTPRetryYAML = `    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`
