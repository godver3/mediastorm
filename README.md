<p align="center">
<img width="400" height="240" alt="image" src="https://github.com/user-attachments/assets/2ef5cb4b-2db7-4b1c-aa54-9bad3a63b12a" />
</p>

# strmr

A streaming media server with native mobile and TV apps. strmr supports:

- Usenet
- Real Debrid/Torbox/AllDebrid

Scraping supports:

- Torrentio
- Jackett
- AIOStreams
- Zilean
- Newznab indexers

Discord: https://discord.gg/kT74mwf4bu

## Setup

strmr requires both a backend server and a frontend app. The frontend app on its own does nothing - it needs a running backend to connect to.

### Backend Deployment

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

The backend will be available at `http://localhost:7777`. The default login is `admin`/`admin` for both the frontend app and the admin web UI.

### Frontend Apps

The frontend is built with React Native and supports iOS, tvOS, Android, and Android TV.

#### iOS / tvOS

Available on TestFlight:

- iOS: [Join TestFlight](https://testflight.apple.com/join/8vCQ5gmH)
- tvOS: [Join TestFlight](https://testflight.apple.com/join/X9bE3dq6)

**Updates:** Incremental updates are delivered automatically via OTA. Larger updates require updating through TestFlight.

#### Android / Android TV

Download the latest APK: [Releases](https://github.com/godver3/strmr/releases)

**Updates:** Incremental updates are delivered automatically via OTA. Larger updates require manually downloading the new APK from [GitHub Releases](https://github.com/godver3/strmr/releases) or using Downloader (code listed with each release).

## Configuration

Access the admin panel at `http://localhost:7777/admin` to configure all settings. Required settings are indicated in the web UI settings page.

## Roadmap

See Discord for more planning details.

- Non-M3U IPTV support
- Mediafusion support

## What to test?

Please test: 

- General searching/streaming/media matching
- Test DV/HDR playback
- Android TV performance

## Acknowledgments

Thanks to [nzbdav](https://github.com/nzbdav-dev/nzbdav) and [altmount](https://github.com/javi11/altmount) for paving the way with usenet streaming.

Inspired by [plex_debrid](https://github.com/itsToggle/plex_debrid) and [Riven](https://github.com/rivenmedia/riven).

Special thanks to [Parsett (PTT)](https://github.com/dreulavelle/PTT) for media title parsing.

## License

MIT
