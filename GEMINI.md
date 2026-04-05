# GEMINI.md - Project Context & Guidelines

## Project Overview
**mediastorm** is a media streaming solution consisting of a self-hosted Go backend and a TV-optimized React Native frontend.

## Tech Stack

### Backend (`/backend`)
- **Language:** Go 1.24
- **Framework:** Standard library + `gorilla/mux` for routing.
- **Architecture:** Service-oriented with manual dependency injection in `main.go`.
- **Database:** SQLite (embedded).
- **Key Modules:**
  - `handlers`: HTTP controllers.
  - `services`: Business logic (Debrid, NZB, Metadata, Playback).
  - `internal`: Core libraries (database, encryption).
- **Authentication:** PIN-based middleware.

### Frontend (`/frontend`)
- **Framework:** React Native (Expo).
- **Navigation:** Expo Router (file-based).
- **Target Platforms:** Android TV, tvOS, iOS, Android (mobile).
- **State Management:** React Context API.
- **Video:** Supports multiple playback engines (e.g., VLC, expo-video).

## Operational Guidelines

### 1. Development Standards
- **File Size Limit:** **Strictly maintain files under 500 lines of code.** Refactor and split large files proactively to ensure maintainability.
- **Version Control:** Commit and push changes to GitHub frequently. Focus on small, incremental, atomic commits rather than massive batch updates.

### 2. Backend Deployment Strategy
**After backend changes are tested and verified:**
1.  Run project-specific tests (`go test ./...`).
2.  Rebuild the Docker image.
3.  Push the updated image to Docker Hub.

### Authenticated Local API Testing
- Most backend endpoints require auth via `Authorization: Bearer <token>`, `X-PIN`, or `?token=`.
- Retrieve a current session token from Docker Postgres with:
  ```bash
  docker exec mediastorm-postgres psql -U mediastorm -d mediastorm -Atc "SELECT token FROM sessions WHERE expires_at > now() ORDER BY created_at DESC LIMIT 1;"
  ```
- Use the token with curl and wrap the header value in single quotes:
  ```bash
  curl -sS -H 'Authorization: Bearer <token>' http://127.0.0.1:7777/api/auth/me
  ```

### 3. Frontend Deployment Strategy
**After frontend changes:**
- **Evaluate the Scope:**
  - **Minor JS/UI/Asset changes:** Push an **Expo Update** (`eas update`) for immediate delivery.
  ## Repository & Workflow Learnings

  ### 1. Nested Repository Structure
  - **Frontend Independence:** The `/frontend` directory is a standalone Git repository.
  - **Root Tracking:** The root repository **must not** track the frontend as a submodule or gitlink. The root `.gitignore` explicitly excludes `frontend/` to maintain this separation.
  - **Commit Workflow:** Frontend changes must be committed within the `/frontend` directory. Root commits should focus on backend, documentation, and project-wide configuration (like `docker-compose.yml`).
  - **Script Hygiene:** Operational and build scripts (e.g., `build-docker.sh`, `dev.sh`) should be ignored in the root repository to keep the version history focused on application logic.

  ## Architectural Conventions
