# strmr

A streaming media server with native mobile and TV apps.

## Backend Deployment

Deploy the backend using Docker Compose:

1. Create a `docker-compose.yml`:

```yaml
services:
  strmr:
    image: godver3/strmr:latest
    container_name: strmr
    ports:
      - "7777:7777"
    volumes:
      - /path/to/your/cache:/root/cache
    environment:
      - TZ=UTC
    restart: unless-stopped
```

2. Start the container:

```bash
docker-compose up -d
```

The backend will be available at `http://localhost:7777`.

## Configuration

Access the admin panel at `http://localhost:7777/admin` to configure settings that are not available in the mobile/TV apps, including:

- Debrid service credentials
- Addon configuration
- Stream settings
- And more

## Frontend Apps

The frontend is built with React Native and supports iOS, tvOS, Android, and Android TV.

### iOS / tvOS

Available on TestFlight: [Join TestFlight](#) *(coming soon)*

### Android / Android TV

Download the latest APK: [Releases](#) *(coming soon)*

## Acknowledgments

Special thanks to [Parsett (PTT)](https://github.com/dreulavelle/PTT) for media title parsing.

## License

MIT
