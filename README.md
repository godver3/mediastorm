# strmr

<img width="1024" height="1024" alt="image" src="https://github.com/user-attachments/assets/69d71ca9-14df-44d2-a10d-f97ffaf9e109" />

A streaming media server with native mobile and TV apps. strmr supports:

- Usenet
- Real Debrid/Torbox

Scraping supports:

- Torrentio
- Newznab

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

The cache folder will contain user settings and stream metadata.

2. Start the container:

```bash
docker-compose up -d
```

The backend will be available at `http://localhost:7777`.

## Configuration

Access the admin panel at `http://localhost:7777/admin` to configure settings that are not available in the mobile/TV apps, including:

- Service credentials
- M3U link

## Roadmap

Current roadmap:

- cli_debrid style filtering
- AIOstreams, Mediafusion, Jackett/Prowlarr support
- Non-M3U IPTV support

## What to test?

Please test: 

- General searching/streaming/media matching
- Test DV/HDR playback
- Android TV performance

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
