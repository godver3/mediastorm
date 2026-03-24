<p align="center">
<img width="400" height="240" alt="mediastorm" src="https://github.com/godver3/mediastorm/blob/master/mediastorm-tv.jpg?raw=true" />
</p>

# mediastorm

A streaming media server with native mobile and TV apps. mediastorm supports:

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

mediastorm requires both a backend server and a frontend app. The frontend app on its own does nothing - it needs a running backend to connect to.

### Backend Deployment

Deploy the backend using Docker Compose (or use the example in the repo):

1. Create a `docker-compose.yml`:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    container_name: mediastorm-postgres
    environment:
      POSTGRES_DB: mediastorm
      POSTGRES_USER: mediastorm
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-mediastorm}
    volumes:
      - postgres_data:/var/lib/postgresql/data
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U mediastorm"]
      interval: 5s
      timeout: 5s
      retries: 5

  mediastorm:
    image: godver3/mediastorm:latest
    container_name: mediastorm
    depends_on:
      postgres:
        condition: service_healthy
    ports:
      - "7777:7777"
    volumes:
      # User data folder containing settings.json, streams, and cache
      - /path/to/your/cache:/root/cache
    environment:
      - TZ=${TZ:-UTC}
      - DATABASE_URL=postgres://mediastorm:${POSTGRES_PASSWORD:-mediastorm}@postgres:5432/mediastorm?sslmode=disable
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "--no-verbose", "--tries=1", "http://localhost:7777/health"]
      interval: 30s
      timeout: 3s
      start_period: 15s
      retries: 3

volumes:
  postgres_data:
```

The cache folder will contain settings.json and stream metadata. All user data (accounts, watch history, playback progress, etc.) is stored in PostgreSQL.

2. Start the containers:

```bash
docker compose up -d
```

The backend will be available at `http://localhost:7777`. The default login is `admin`/`admin` for both the frontend app and the admin web UI.

**Upgrading from a previous version:** On first startup with the new compose file, mediastorm will automatically migrate your existing JSON data into PostgreSQL. Your original JSON files are preserved with a `.migrated` suffix in the cache directory.

**Custom Postgres password:** Set the `POSTGRES_PASSWORD` environment variable before starting:

```bash
POSTGRES_PASSWORD=your_secure_password docker compose up -d
```

> **⚠️ Notice:** mediastorm is developed with the assistance of large language models (LLMs). While best efforts have been made to ensure security and code integrity, use this software at your own risk. mediastorm is not designed to be directly exposed to the internet — for safe remote access, use a VPN or overlay network like [Tailscale](https://tailscale.com/) to keep your server private while still accessible from your devices.

### Frontend Apps

The frontend is built with React Native and supports iOS, tvOS, Android, and Android TV.

#### iOS / tvOS

Available on TestFlight:

- iOS: [Join TestFlight](https://testflight.apple.com/join/8vCQ5gmH)
- tvOS: [Join TestFlight](https://testflight.apple.com/join/X9bE3dq6)

**Updates:** Incremental updates are delivered automatically via OTA. Larger updates require updating through TestFlight.

#### Android / Android TV

Download the latest APK: [Releases](https://github.com/godver3/mediastorm/releases)

**Updates:** Incremental updates are delivered automatically via OTA. Larger updates require manually downloading the new APK from [GitHub Releases](https://github.com/godver3/mediastorm/releases) or using Downloader (code listed with each release).

## Configuration

Access the admin panel at `http://localhost:7777/admin` to configure all settings.

### Required API Keys

mediastorm requires API keys from TMDB and TVDB for metadata (posters, descriptions, cast info, etc.):

| Service | Required | Purpose | Get Your Key |
|---------|----------|---------|--------------|
| **TMDB** | ✅ Yes | Movie/TV metadata, posters, cast | [themoviedb.org/settings/api](https://www.themoviedb.org/settings/api) (free account) |
| **TVDB** | ✅ Yes | TV show metadata, episode info | [thetvdb.com/api-information](https://thetvdb.com/api-information) (free account) |
| **MDBList** | ❌ Optional | Ratings from multiple sources (IMDb, RT, etc.) | [mdblist.com/preferences](https://mdblist.com/preferences/) (free account) |
| **Gemini** | ❌ Optional | AI-powered personalized recommendations | [aistudio.google.com/apikey](https://aistudio.google.com/apikey) (free tier) |

Enter these keys in the admin panel under **Settings → Metadata**.

### AI Recommendations (Gemini)

mediastorm can use Google's Gemini AI to generate personalized "Recommended For You" lists based on your watch history and watchlist. This is entirely optional — without a key, mediastorm still provides TMDB-based "Because you watched..." recommendations.

**Setup:**

1. Go to [Google AI Studio](https://aistudio.google.com/apikey) and sign in with a Google account
2. Click **Create API Key** and copy it
3. In the mediastorm admin panel, go to **Settings → Metadata** and paste the key into the **Gemini API Key** field
4. Save — recommendations will appear in the **Lists** tab under "Recommended For You"

**Cost:** Gemini 2.0 Flash is used, which has a generous free tier (1,500 requests/day). A typical user generates ~1 request per day (results are cached for 24 hours per user), so this should remain free for personal use.

## Acknowledgments

Thanks to [nzbdav](https://github.com/nzbdav-dev/nzbdav) and [altmount](https://github.com/javi11/altmount) for paving the way with usenet streaming.

Inspired by [plex_debrid](https://github.com/itsToggle/plex_debrid) and [Riven](https://github.com/rivenmedia/riven).

Special thanks to [Parsett (PTT)](https://github.com/dreulavelle/PTT) for media title parsing.

Powered by [FFmpeg](https://ffmpeg.org/) for media processing and [yt-dlp](https://github.com/yt-dlp/yt-dlp) for trailer fetching.

Native playback powered by [KSPlayer](https://github.com/kingslay/KSPlayer) on iOS/tvOS, [ExoPlayer](https://github.com/google/ExoPlayer) and [MPV](https://mpv.io/) on Android/Android TV.

## License

MIT

## Links

[![Hypercommit](https://img.shields.io/badge/Hypercommit-DB2475)](https://hypercommit.com/mediastorm)
