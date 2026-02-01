# Tunnelr

A self-hosted localhost tunnel. Expose your local development server to the internet with your own domain.

**Why Tunnelr?**

- **Self-hosted** - Your data stays on your infrastructure
- **No limits** - Unlimited tunnels, no time restrictions
- **Simple** - One command to deploy, one command to connect
- **Flexible** - Subdomain or path-based routing

## Quick Start

### 1. Deploy the Server

On your VPS:

```bash
# Install Docker (if not already installed)
curl -fsSL https://get.docker.com | sh

# Clone the repo
git clone https://github.com/Aakeeo/tunnelr.git
cd tunnelr

# Configure
cp .env.example .env
nano .env  # Set your domain and email

# Start the server
docker compose up -d

# Verify deployment
curl https://yourdomain.com/status
```

### 2. Connect from Your Machine

```bash
# Build the CLI
go build -o tunnelr ./cmd/cli

# Connect to your server
TUNNELR_SERVER=wss://yourdomain.com/ws ./tunnelr connect 3000
```

You'll see:
```
Tunnel established!

  Public URL:  https://yourdomain.com/t/a1b2c3
  Forwarding:  https://yourdomain.com/t/a1b2c3 -> http://localhost:3000

Press Ctrl+C to close the tunnel
```

## Configuration

All configuration is done via environment variables in `.env`:

| Variable | Description | Default |
|----------|-------------|---------|
| `BASE_DOMAIN` | Your domain (e.g., `tunnel.example.com`) | `localhost` |
| `ROUTING_MODE` | `subdomain` or `path` (see below) | `subdomain` |
| `SSL_EMAIL` | Email for Let's Encrypt certificates | - |

### Routing Modes

**Path Mode** (recommended for easy setup)
```
ROUTING_MODE=path
```
- URLs: `https://yourdomain.com/t/abc123/webhook`
- Requires: Only main domain DNS
- SSL works automatically (no wildcard cert needed)

**Subdomain Mode** (cleaner URLs)
```
ROUTING_MODE=subdomain
```
- URLs: `https://abc123.yourdomain.com/webhook`
- Requires: Wildcard DNS + wildcard SSL certificate
- Better compatibility with some webhook providers
- See [Wildcard SSL Setup](#wildcard-ssl-setup) for configuration

## DNS Setup

### For Path Mode (Recommended)

Add one A record:
```
yourdomain.com      →  YOUR_SERVER_IP
```

Caddy automatically obtains SSL certificate via HTTP-01 challenge. No additional setup needed.

### For Subdomain Mode

Add two A records:
```
yourdomain.com      →  YOUR_SERVER_IP
*.yourdomain.com    →  YOUR_SERVER_IP
```

> **Note:** Subdomain mode requires a wildcard SSL certificate. See [Wildcard SSL Setup](#wildcard-ssl-setup) below.

## SSL Certificates

### Path Mode
SSL works automatically. Caddy obtains certificates from Let's Encrypt using HTTP-01 challenge.

### Subdomain Mode (Wildcard SSL)

Wildcard certificates (`*.yourdomain.com`) require DNS-01 challenge. Caddy needs API access to your DNS provider to create verification records.

**Option 1: Use Cloudflare (Recommended)**

1. Move your domain's nameservers to Cloudflare (free)
2. Get a Cloudflare API token with DNS edit permissions
3. Update `docker-compose.yml`:
```yaml
caddy:
  image: caddy:2-alpine
  # ... existing config ...
  environment:
    - CLOUDFLARE_API_TOKEN=your_token_here
```

4. Update `Caddyfile`:
```
*.{$BASE_DOMAIN} {
    tls {
        dns cloudflare {env.CLOUDFLARE_API_TOKEN}
    }
    reverse_proxy server:8080 {
        header_up Host {host}
    }
}
```

5. Rebuild with Cloudflare DNS plugin:
```dockerfile
FROM caddy:2-builder AS builder
RUN xcaddy build --with github.com/caddy-dns/cloudflare

FROM caddy:2-alpine
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
```

**Option 2: Use Path Mode**

If you don't want to set up DNS-01 challenge, use path mode instead:
```
ROUTING_MODE=path
```

URLs will be `https://yourdomain.com/t/<tunnel-id>/...` - no wildcard cert needed.

## Verifying Setup

After deployment, check if everything is configured correctly:

```bash
curl https://yourdomain.com/status
```

Response:
```json
{
  "ready": true,
  "message": "Ready! Tunnel URLs: https://<tunnel-id>.yourdomain.com/...",
  "base_domain": "yourdomain.com",
  "routing_mode": "subdomain",
  "active_tunnels": 0,
  "domain_check": {"ok": true, "ips": ["167.99.x.x"]},
  "wildcard_check": {"ok": true, "ips": ["167.99.x.x"]}
}
```

If there are issues, the `message` field will tell you what to fix.

## CLI Usage

```bash
# Expose localhost:3000
tunnelr connect 3000

# Expose a different port
tunnelr connect 8080

# Show help
tunnelr help
```

### Connecting to a Custom Server

By default, the CLI connects to `ws://localhost:8080/ws`. To connect to your deployed server:

```bash
TUNNELR_SERVER=wss://yourdomain.com/ws tunnelr connect 3000
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         YOUR SERVER (VPS)                           │
│                                                                     │
│    ┌─────────┐      ┌──────────────┐      ┌──────────────┐         │
│    │  Caddy  │─────▶│Tunnel Server │◀────▶│   Registry   │         │
│    │ (HTTPS) │      │    (Go)      │      │ (in-memory)  │         │
│    └─────────┘      └──────────────┘      └──────────────┘         │
│         ▲                  ▲                                        │
└─────────│──────────────────│────────────────────────────────────────┘
          │                  │
          │ HTTPS            │ WebSocket
          │                  │
    ┌─────┴─────┐      ┌─────┴─────┐
    │  Webhook  │      │  Tunnelr  │
    │  Provider │      │    CLI    │
    │ (Stripe)  │      │           │
    └───────────┘      └─────┬─────┘
                             │
                             ▼
                     ┌─────────────┐
                     │ localhost   │
                     │   :3000     │
                     └─────────────┘
```

**Flow:**
1. CLI connects to server via WebSocket
2. Server assigns a unique tunnel ID
3. Webhook provider sends request to `https://abc123.yourdomain.com`
4. Caddy terminates SSL, forwards to tunnel server
5. Server finds the tunnel by subdomain/path, forwards request via WebSocket
6. CLI receives request, forwards to localhost
7. Response travels back the same path

## Development

### Prerequisites

- Go 1.21+
- Docker & Docker Compose (for server deployment)

### Running Locally

```bash
# Terminal 1: Start the server
go run ./cmd/server

# Terminal 2: Start a test HTTP server
python3 -m http.server 3000

# Terminal 3: Start the CLI
go run ./cmd/cli connect 3000

# Terminal 4: Test the tunnel
curl -H "Host: <tunnel-id>.localhost" http://localhost:8080/
```

### Testing Path Mode

```bash
# Start server in path mode
ROUTING_MODE=path go run ./cmd/server

# Test
curl http://localhost:8080/t/<tunnel-id>/
```

### Building

```bash
# Build server
go build -o server ./cmd/server

# Build CLI
go build -o tunnelr ./cmd/cli

# Build for multiple platforms
GOOS=darwin GOARCH=amd64 go build -o tunnelr-mac ./cmd/cli
GOOS=linux GOARCH=amd64 go build -o tunnelr-linux ./cmd/cli
GOOS=windows GOARCH=amd64 go build -o tunnelr.exe ./cmd/cli
```

## Project Structure

```
tunnelr/
├── cmd/
│   ├── server/          # Tunnel server
│   │   └── main.go
│   └── cli/             # CLI client
│       └── main.go
├── internal/
│   └── tunnel/          # Shared tunnel logic
│       ├── protocol.go  # Message types
│       └── registry.go  # Tunnel registry
├── Dockerfile           # Server container
├── docker-compose.yml   # Production deployment
├── Caddyfile            # Reverse proxy config
├── .env.example         # Configuration template
└── README.md
```

## Comparison with Alternatives

| Feature | Tunnelr | ngrok (free) | Cloudflare Tunnel |
|---------|---------|--------------|-------------------|
| Self-hosted | ✅ | ❌ | ❌ |
| Unlimited tunnels | ✅ | ❌ (1) | ✅ |
| Persistent URLs | ✅ | ❌ (2hr) | ✅ |
| Custom domain | ✅ | ❌ | ✅ |
| No account required | ✅ | ❌ | ❌ |
| Data privacy | ✅ | ❌ | ❌ |

## License

MIT License - see [LICENSE](LICENSE) for details.
