# tsnet-proxy

A powerful Tailscale-native reverse proxy that allows you to expose multiple Docker containers as separate Tailscale devices from a single container. Built with [tsnet](https://tailscale.com/kb/1244/tsnet), the Go library that embeds Tailscale directly into applications.

## Features

- 🚀 **Multi-Service Support** - Proxy multiple backend services from a single container
- 🔒 **Zero-Trust Security** - Each service appears as a separate Tailscale device with independent ACLs
- 🎛️ **Web UI** - Manage services through a beautiful web interface
- ❤️ **Health Checks** - Automatic health monitoring with circuit breaker pattern
- 📊 **Prometheus Metrics** - Built-in metrics for monitoring and alerting
- 🛣️ **Path-Based Routing** - Route specific URL paths to different backends
- 🔐 **TLS Backend Support** - Proxy to HTTPS backends with cert verification options
- 📝 **Dynamic Configuration** - Add/remove services without restarting
- 🐳 **Docker Native** - Access containers on Docker network seamlessly

## Why tsnet-proxy?

Traditional Tailscale setups require one Tailscale container per service, leading to:
- Container sprawl (10+ sidecar containers)
- Complex docker-compose configurations
- Resource overhead
- Management headaches

**tsnet-proxy solves this** by creating multiple Tailscale "devices" (one per service) from a single container, each with:
- Unique hostname (`grafana.your-tailnet.ts.net`, `api.your-tailnet.ts.net`, etc.)
- Independent TLS certificates
- Separate ACL policies
- Health monitoring

## Quick Start

### Prerequisites

1. A [Tailscale account](https://login.tailscale.com/start)
2. Docker and Docker Compose installed
3. A Tailscale auth key ([generate one here](https://login.tailscale.com/admin/settings/keys))

### Installation

1. **Clone the repository:**
   ```bash
   git clone https://github.com/NathanBhanji/tsnet-proxy.git
   cd tsnet-proxy
   ```

2. **Set your Tailscale auth key:**
   ```bash
   export TS_AUTHKEY=tskey-auth-YOUR-KEY-HERE
   ```

3. **Start the stack:**
   ```bash
   docker-compose up -d
   ```

4. **Access the management UI:**

   The UI will appear as a new device in your [Tailscale admin panel](https://login.tailscale.com/admin/machines). Access it at:
   ```
   http://tsnet-proxy-docker.your-tailnet.ts.net
   ```

That's it! The example configuration includes Grafana, which will be available at `https://grafana.your-tailnet.ts.net`.

## Configuration

### Config File (`configs/services.yaml`)

```yaml
# Global settings
authKey: "${TS_AUTHKEY}"           # Tailscale auth key (from environment)
stateDir: "/data/tsnet"             # Persistent state directory

# Management UI
managementUI:
  enabled: true
  hostname: "tsnet-proxy-docker"    # UI hostname on tailnet
  port: 8080

# Prometheus metrics
metrics:
  enabled: true
  port: 9090

# Services to proxy
services:
  - name: "grafana"                 # Tailscale hostname
    backend: "http://grafana:3000"  # Docker service URL
    paths: []                        # Empty = proxy all paths
    stripPrefix: false
    healthCheck:
      enabled: true
      path: "/api/health"
      interval: 30s
      timeout: 5s
      unhealthyThreshold: 3
    tls:
      enabled: false                # Backend uses HTTP

  - name: "api"                     # Another service
    backend: "http://api-server:8080"
    paths:
      - "/api"                      # Only proxy /api/* requests
    stripPrefix: true               # Remove /api prefix before forwarding
    healthCheck:
      enabled: true
      path: "/health"
      interval: 10s
      timeout: 3s
      unhealthyThreshold: 2
    tls:
      enabled: false
```

### Service Configuration Options

| Field | Description | Required |
|-------|-------------|----------|
| `name` | Tailscale hostname (must be lowercase, alphanumeric, hyphens only) | Yes |
| `backend` | Backend URL (e.g., `http://service:port`) | Yes |
| `paths` | URL path prefixes to match (empty = match all) | No |
| `stripPrefix` | Remove matched path prefix before forwarding | No |
| `healthCheck.enabled` | Enable health checking | No |
| `healthCheck.path` | Health check endpoint path | If enabled |
| `healthCheck.interval` | Check interval (e.g., `30s`, `1m`) | No |
| `healthCheck.timeout` | Request timeout | No |
| `healthCheck.unhealthyThreshold` | Failures before marking unhealthy | No |
| `tls.enabled` | Backend uses HTTPS | No |
| `tls.skipVerify` | Skip TLS certificate verification (insecure) | No |

## Usage

### Adding Services via UI

1. Navigate to the management UI
2. Click "Add Service"
3. Fill in the form:
   - **Service Name**: The Tailscale hostname (e.g., `grafana`)
   - **Backend URL**: Docker service URL (e.g., `http://grafana:3000`)
   - **Paths**: Optional path prefixes (e.g., `/api, /grafana`)
   - Configure health checks and TLS as needed
4. Click "Add Service"

The service will appear immediately in your Tailscale network!

### Adding Services via Config File

Edit `configs/services.yaml` and restart:
```bash
docker-compose restart tsnet-proxy
```

### Path-Based Routing Example

Route different paths to different backends:

```yaml
services:
  - name: "myapp"
    backend: "http://api:8080"
    paths: ["/api"]
    stripPrefix: true

  - name: "myapp-web"
    backend: "http://frontend:3000"
    paths: ["/"]
    stripPrefix: false
```

Access patterns:
- `https://myapp.tailnet.ts.net/api/users` → `http://api:8080/users` (prefix stripped)
- `https://myapp-web.tailnet.ts.net/` → `http://frontend:3000/`

### Health Checks

Health checks automatically mark services unhealthy after consecutive failures:

```yaml
healthCheck:
  enabled: true
  path: "/health"               # Endpoint to check
  interval: 30s                  # Check every 30 seconds
  timeout: 5s                    # Fail if no response in 5s
  unhealthyThreshold: 3          # Mark unhealthy after 3 failures
```

Unhealthy services return `503 Service Unavailable` until they recover.

### HTTPS Backends

For backends using HTTPS:

```yaml
tls:
  enabled: true
  skipVerify: false              # Set true for self-signed certs (insecure)
```

## Monitoring

### Prometheus Metrics

Metrics are exposed at `http://localhost:9090/metrics` (inside container):

```yaml
# Request metrics
tsnet_proxy_requests_total{service, method, status}
tsnet_proxy_request_duration_seconds{service, method}

# Health metrics
tsnet_proxy_service_health{service}  # 1 = healthy, 0 = unhealthy

# Connection metrics
tsnet_proxy_active_connections{service}
```

### Sample Prometheus Config

```yaml
scrape_configs:
  - job_name: 'tsnet-proxy'
    static_configs:
      - targets: ['tsnet-proxy:9090']
```

### Grafana Dashboard

Import the included Grafana dashboard (coming soon) or create custom panels:
- Request rate by service
- P95 latency
- Error rate
- Service health status

## Docker Compose Examples

### Basic Setup

```yaml
version: '3.8'
services:
  tsnet-proxy:
    image: nathanbhanji/tsnet-proxy:latest
    environment:
      - TS_AUTHKEY=${TS_AUTHKEY}
    volumes:
      - ./configs/services.yaml:/app/configs/services.yaml:rw
      - tsnet-data:/data/tsnet
    networks:
      - services

  myapp:
    image: myapp:latest
    networks:
      - services

networks:
  services:
volumes:
  tsnet-data:
```

### Multiple Services

```yaml
services:
  tsnet-proxy:
    # ... (same as above)

  grafana:
    image: grafana/grafana:latest
    networks:
      - services

  prometheus:
    image: prom/prometheus:latest
    networks:
      - services

  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: secret
    networks:
      - services
```

Then add each service via the UI or config file!

## Security Best Practices

1. **Use reusable auth keys**: Generate non-ephemeral keys for persistent devices
2. **Set up ACLs**: Configure [Tailscale ACLs](https://tailscale.com/kb/1018/acls) to restrict access
3. **Don't expose ports**: Let tsnet-proxy handle all access, don't bind backend ports
4. **Verify TLS certs**: Set `tls.skipVerify: false` for production backends
5. **Use health checks**: Enable health checking to automatically stop routing to failed services
6. **Monitor metrics**: Set up alerting on health and error rate metrics

## Troubleshooting

### Service not appearing in Tailscale

1. Check logs: `docker-compose logs tsnet-proxy`
2. Verify auth key is set: `echo $TS_AUTHKEY`
3. Check Tailscale state directory has correct permissions
4. Wait 30-60 seconds for initial connection to complete

### Health checks failing

1. Verify backend URL is correct and accessible from container
2. Check health endpoint returns 2xx/3xx status code
3. Adjust timeout/interval if backend is slow
4. View health status in management UI

### Cannot access management UI

1. Check `managementUI.enabled: true` in config
2. Verify UI device appears in Tailscale admin panel
3. Try accessing via Tailscale IP: `http://100.x.x.x:8080`
4. Check logs for UI startup errors

### Backend connection errors

1. Verify backend container is on same Docker network
2. Test connectivity: `docker exec tsnet-proxy ping backend-service`
3. Check backend logs for errors
4. Verify TLS settings match backend (HTTP vs HTTPS)

## Development

### Building from Source

```bash
# Clone repository
git clone https://github.com/NathanBhanji/tsnet-proxy.git
cd tsnet-proxy

# Install dependencies
go mod download

# Build binary
go build -o tsnet-proxy ./cmd/tsnet-proxy

# Run locally
export TS_AUTHKEY=tskey-auth-YOUR-KEY
./tsnet-proxy --config configs/services.yaml
```

### Project Structure

```
tsnet-proxy/
├── cmd/
│   └── tsnet-proxy/
│       └── main.go              # Application entry point
├── internal/
│   ├── config/                  # Configuration parsing
│   ├── manager/                 # Service lifecycle management
│   ├── proxy/                   # Reverse proxy logic
│   ├── health/                  # Health checking system
│   ├── ui/                      # Web UI and API
│   └── metrics/                 # Prometheus metrics
├── configs/
│   └── services.yaml            # Default configuration
├── Dockerfile                    # Container image
├── docker-compose.yml            # Docker Compose setup
└── README.md                     # This file
```

## Roadmap

- [ ] WebSocket support
- [ ] Load balancing (multiple backends per service)
- [ ] Rate limiting
- [ ] Request/response logging
- [ ] Grafana dashboard template
- [ ] Hot reload configuration
- [ ] CLI for management
- [ ] Helm chart for Kubernetes

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## License

MIT License - see [LICENSE](LICENSE) file for details

## Acknowledgments

- Built with [tsnet](https://tailscale.com/kb/1244/tsnet) by Tailscale
- Inspired by [tsnsrv](https://github.com/boinkor-net/tsnsrv) and [tsnet-composable](https://github.com/n-elderbroom/tsnet-composable)
- UI built with [Tailwind CSS](https://tailwindcss.com)

## Support

- 📚 [Documentation](https://github.com/NathanBhanji/tsnet-proxy/wiki)
- 🐛 [Issue Tracker](https://github.com/NathanBhanji/tsnet-proxy/issues)
- 💬 [Discussions](https://github.com/NathanBhanji/tsnet-proxy/discussions)
- 🔧 [Tailscale Support](https://tailscale.com/contact/support)

---

**Made with ❤️ for the Tailscale community**
