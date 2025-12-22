<p align="center">
<img width="400" height="240" alt="image" src="https://github.com/user-attachments/assets/2ef5cb4b-2db7-4b1c-aa54-9bad3a63b12a" />
</p>

# strmr

A streaming media server with native mobile and TV apps. strmr supports:

- Usenet
- Real Debrid/Torbox

Scraping supports:

- Torrentio
- Newznab

Discord: https://discord.gg/kT74mwf4bu

## Backend Deployment

Deploy the backend using Docker Compose (or use the example in the repo):

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

The backend will be available at `http://localhost:7777`. The backend logs will include a generated PIN to use to connect the frontend to the backend. You will need to add the PIN and your backend URL in the frontend app.

## Configuration

Access the admin panel at `http://localhost:7777/admin` to configure settings that are not available in the mobile/TV apps, including:

- Service credentials
- M3U link

Required settings are indicated in the web UI settings page.

## Roadmap

Current roadmap:

- cli_debrid style filtering
- Fine-grained ranking
- AIOstreams, Mediafusion, Jackett/Prowlarr support
- Non-M3U IPTV support
- Custom shelf content
- View Watch History

## What to test?

Please test: 

- General searching/streaming/media matching
- Test DV/HDR playback
- Android TV performance

## Frontend Apps

The frontend is built with React Native and supports iOS, tvOS, Android, and Android TV. Updates will be pushed through Expo OTA and auto update testing apps (1.0.x). New builds will be submitted periodically as major increments (1.x.0). Update details will be shared in the Discord. 

### iOS / tvOS

Available on TestFlight

- iOS: [Join TestFlight](https://testflight.apple.com/join/8vCQ5gmH)
- tvOS: [Join TestFlight](https://testflight.apple.com/join/X9bE3dq6)

### Android / Android TV

Download the latest APK: [Releases](https://github.com/godver3/strmr/releases)

## Acknowledgments

Thanks to [nzbdav](https://github.com/nzbdav-dev/nzbdav) and [altmount](https://github.com/javi11/altmount) for paving the way with usenet streaming.

Inspired by [plex_debrid](https://github.com/itsToggle/plex_debrid) and [riven](https://github.com/rivenmedia/riven).

Special thanks to [Parsett (PTT)](https://github.com/dreulavelle/PTT) for media title parsing.

## License

MIT
