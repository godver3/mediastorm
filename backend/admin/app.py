#!/usr/bin/env python3
"""
NovaStream Admin Panel
A Flask-based web interface for managing NovaStream settings and monitoring server status.
"""

import json
import os
import time
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, Optional

import requests
from flask import Flask, jsonify, render_template, request

app = Flask(__name__)

# Configuration
CONFIG_PATH = os.environ.get("STRMR_CONFIG", os.environ.get("NOVASTREAM_CONFIG", "/root/strmr/backend/cache/settings.json"))
BACKEND_HOST = os.environ.get("NOVASTREAM_BACKEND_HOST", "localhost")
BACKEND_PORT = int(os.environ.get("NOVASTREAM_BACKEND_PORT", "7777"))
ADMIN_PORT = int(os.environ.get("NOVASTREAM_ADMIN_PORT", "7778"))

# Settings schema for dynamic form generation
# This defines metadata about each setting for the admin UI
SETTINGS_GROUPS = [
    {"id": "server", "label": "Server"},
    {"id": "providers", "label": "Providers"},
    {"id": "sources", "label": "Sources"},
    {"id": "experience", "label": "Experience"},
    {"id": "storage", "label": "Storage & Data"},
]

SETTINGS_SCHEMA = {
    "server": {
        "label": "Server Settings",
        "icon": "server",
        "group": "server",
        "order": 0,
        "fields": {
            "host": {"type": "text", "label": "Host", "description": "Server bind address"},
            "port": {"type": "number", "label": "Port", "description": "Server port"},
            "pin": {"type": "password", "label": "PIN", "description": "6-digit authentication PIN"},
        }
    },
    "streaming": {
        "label": "Streaming",
        "icon": "play-circle",
        "group": "providers",
        "order": 0,
        "fields": {
            "maxDownloadWorkers": {"type": "number", "label": "Max Download Workers", "description": "Maximum concurrent download workers"},
            "maxCacheSizeMB": {"type": "number", "label": "Max Cache Size (MB)", "description": "Maximum cache size in megabytes"},
            "serviceMode": {"type": "select", "label": "Service Mode", "options": ["usenet", "debrid", "hybrid"], "description": "Streaming service mode"},
            "servicePriority": {"type": "select", "label": "Service Priority", "description": "Prioritize results from a specific service type", "options": ["none", "usenet", "debrid"]},
        }
    },
    "debridProviders": {
        "label": "Debrid Providers",
        "icon": "cloud",
        "group": "providers",
        "order": 1,
        "is_array": True,
        "parent": "streaming",
        "key": "debridProviders",
        "fields": {
            "name": {"type": "text", "label": "Name", "description": "Provider display name"},
            "provider": {"type": "select", "label": "Provider", "options": ["realdebrid", "torbox"], "description": "Provider type"},
            "apiKey": {"type": "password", "label": "API Key", "description": "Provider API key"},
            "enabled": {"type": "boolean", "label": "Enabled", "description": "Enable this provider"},
        }
    },
    "usenet": {
        "label": "Usenet Providers",
        "icon": "download",
        "group": "providers",
        "order": 2,
        "is_array": True,
        "fields": {
            "name": {"type": "text", "label": "Name", "description": "Provider name"},
            "host": {"type": "text", "label": "Host", "description": "NNTP server hostname"},
            "port": {"type": "number", "label": "Port", "description": "NNTP port (usually 119 or 563)"},
            "ssl": {"type": "boolean", "label": "SSL", "description": "Use SSL/TLS connection"},
            "username": {"type": "text", "label": "Username", "description": "NNTP username"},
            "password": {"type": "password", "label": "Password", "description": "NNTP password"},
            "connections": {"type": "number", "label": "Connections", "description": "Max connections"},
            "enabled": {"type": "boolean", "label": "Enabled", "description": "Enable this provider"},
        }
    },
    "filtering": {
        "label": "Content Filtering",
        "icon": "filter",
        "group": "sources",
        "order": 0,
        "fields": {
            "maxSizeMovieGb": {"type": "number", "label": "Max Movie Size (GB)", "description": "Maximum movie file size (0 = no limit)"},
            "maxSizeEpisodeGb": {"type": "number", "label": "Max Episode Size (GB)", "description": "Maximum episode file size (0 = no limit)"},
            "hdrDvPolicy": {
                "type": "select",
                "label": "HDR/DV Policy",
                "options": [
                    {"value": "none", "label": "No exclusion"},
                    {"value": "hdr", "label": "Include HDR"},
                    {"value": "hdr_dv", "label": "Include HDR/DV"},
                ],
                "description": "HDR/DV inclusion policy. 'Include HDR' includes DV profile 7/8 (with HDR fallback layer). 'Include HDR/DV' includes all Dolby Vision profiles.",
            },
            "prioritizeHdr": {"type": "boolean", "label": "Prioritize HDR", "description": "Prioritize HDR/DV content in results"},
            "filterOutTerms": {"type": "tags", "label": "Filter Terms", "description": "Terms to filter out from results"},
        }
    },
    "live": {
        "label": "Live TV",
        "icon": "tv",
        "group": "sources",
        "order": 1,
        "fields": {
            "playlistUrl": {"type": "text", "label": "Playlist URL", "description": "M3U playlist URL"},
            "playlistCacheTtlHours": {"type": "number", "label": "Cache TTL (hours)", "description": "Playlist cache duration"},
        }
    },
    "indexers": {
        "label": "Indexers",
        "icon": "search",
        "group": "sources",
        "order": 2,
        "is_array": True,
        "fields": {
            "name": {"type": "text", "label": "Name", "description": "Indexer name"},
            "url": {"type": "text", "label": "URL", "description": "Indexer API URL"},
            "apiKey": {"type": "password", "label": "API Key", "description": "Indexer API key"},
            "type": {"type": "select", "label": "Type", "options": ["torznab"], "description": "Indexer type"},
            "enabled": {"type": "boolean", "label": "Enabled", "description": "Enable this indexer"},
        }
    },
    "torrentScrapers": {
        "label": "Torrent Scrapers",
        "icon": "magnet",
        "group": "sources",
        "order": 3,
        "is_array": True,
        "fields": {
            "name": {"type": "text", "label": "Name", "description": "Scraper name"},
            "type": {"type": "select", "label": "Type", "options": ["torrentio"], "description": "Scraper type"},
            "enabled": {"type": "boolean", "label": "Enabled", "description": "Enable this scraper"},
        }
    },
    "playback": {
        "label": "Playback",
        "icon": "play",
        "group": "experience",
        "order": 0,
        "fields": {
            "preferredPlayer": {"type": "select", "label": "Preferred Player", "options": ["native", "vlc", "infuse", "outplayer"], "description": "Default video player"},
            "preferredAudioLanguage": {"type": "text", "label": "Audio Language", "description": "Preferred audio language code"},
            "preferredSubtitleLanguage": {"type": "text", "label": "Subtitle Language", "description": "Preferred subtitle language code"},
            "preferredSubtitleMode": {"type": "select", "label": "Subtitle Mode", "options": ["off", "on", "auto"], "description": "Default subtitle behavior"},
            "useLoadingScreen": {"type": "boolean", "label": "Loading Screen", "description": "Show loading screen during playback init"},
        }
    },
    "homeShelves": {
        "label": "Home Shelves",
        "icon": "layout",
        "group": "experience",
        "order": 1,
        "fields": {}
    },
    "homeShelves.shelves": {
        "label": "Shelf Configuration",
        "icon": "list",
        "is_array": True,
        "parent": "homeShelves",
        "key": "shelves",
        "fields": {
            "name": {"type": "text", "label": "Name", "description": "Display name"},
            "enabled": {"type": "boolean", "label": "Enabled", "description": "Show this shelf"},
            "order": {"type": "number", "label": "Order", "description": "Sort order (lower = first)"},
        }
    },
    "metadata": {
        "label": "Metadata",
        "icon": "film",
        "group": "storage",
        "order": 0,
        "fields": {
            "tvdbApiKey": {"type": "password", "label": "TVDB API Key", "description": "TheTVDB API key"},
            "tmdbApiKey": {"type": "password", "label": "TMDB API Key", "description": "TheMovieDB API key"},
        }
    },
    "cache": {
        "label": "Cache",
        "icon": "database",
        "group": "storage",
        "order": 1,
        "fields": {
            "directory": {"type": "text", "label": "Directory", "description": "Cache directory path"},
            "metadataTtlHours": {"type": "number", "label": "Metadata TTL (hours)", "description": "Metadata cache duration"},
        }
    },
    "import": {
        "label": "Import Settings",
        "icon": "upload",
        "group": "storage",
        "order": 2,
        "fields": {
            "rarMaxWorkers": {"type": "number", "label": "RAR Max Workers", "description": "Maximum RAR extraction workers"},
            "rarMaxCacheSizeMb": {"type": "number", "label": "RAR Cache Size (MB)", "description": "RAR cache size"},
            "rarMaxMemoryGB": {"type": "number", "label": "RAR Max Memory (GB)", "description": "Maximum memory for RAR operations"},
        }
    },
}


def load_settings() -> Dict[str, Any]:
    """Load settings from the JSON config file."""
    try:
        with open(CONFIG_PATH, "r") as f:
            return json.load(f)
    except FileNotFoundError:
        return {}
    except json.JSONDecodeError as e:
        app.logger.error(f"Failed to parse settings: {e}")
        return {}


def save_settings(settings: Dict[str, Any]) -> bool:
    """Save settings to the JSON config file."""
    try:
        # Write to temp file first, then rename (atomic operation)
        temp_path = CONFIG_PATH + ".tmp"
        with open(temp_path, "w") as f:
            json.dump(settings, f, indent=2)
        os.rename(temp_path, CONFIG_PATH)
        return True
    except Exception as e:
        app.logger.error(f"Failed to save settings: {e}")
        return False


def get_backend_status() -> Dict[str, Any]:
    """Get status from the main NovaStream backend."""
    settings = load_settings()
    pin = settings.get("server", {}).get("pin", "")

    status = {
        "backend_reachable": False,
        "timestamp": datetime.now().isoformat(),
        "uptime": None,
        "active_streams": 0,
        "usenet_connections": {"active": 0, "total": 0},
        "debrid_status": "unknown",
        "cache_usage": {"used_mb": 0, "max_mb": 0},
    }

    try:
        # Try to reach the backend health endpoint
        response = requests.get(
            f"http://{BACKEND_HOST}:{BACKEND_PORT}/health",
            timeout=5
        )
        if response.status_code == 200:
            status["backend_reachable"] = True

        # Get settings to check configuration
        headers = {"X-PIN": pin} if pin else {}
        settings_response = requests.get(
            f"http://{BACKEND_HOST}:{BACKEND_PORT}/api/settings",
            headers=headers,
            timeout=5
        )
        if settings_response.status_code == 200:
            backend_settings = settings_response.json()

            # Calculate usenet connections from settings
            usenet_providers = backend_settings.get("usenet", [])
            total_connections = sum(
                p.get("connections", 0)
                for p in usenet_providers
                if p.get("enabled", False)
            )
            status["usenet_connections"]["total"] = total_connections

            # Get cache settings
            streaming = backend_settings.get("streaming", {})
            status["cache_usage"]["max_mb"] = streaming.get("maxCacheSizeMB", 0)

            # Check debrid providers
            debrid_providers = streaming.get("debridProviders", [])
            enabled_debrid = [p for p in debrid_providers if p.get("enabled")]
            if enabled_debrid:
                status["debrid_status"] = f"{len(enabled_debrid)} provider(s) configured"
            else:
                status["debrid_status"] = "No providers enabled"

    except requests.exceptions.RequestException as e:
        app.logger.warning(f"Failed to reach backend: {e}")

    return status


def get_value_from_path(data: Dict, path: str) -> Any:
    """Get a value from nested dict using dot notation path."""
    keys = path.split(".")
    for key in keys:
        if isinstance(data, dict):
            data = data.get(key, {})
        else:
            return None
    return data


def set_value_at_path(data: Dict, path: str, value: Any) -> None:
    """Set a value in nested dict using dot notation path."""
    keys = path.split(".")
    for key in keys[:-1]:
        if key not in data:
            data[key] = {}
        data = data[key]
    data[keys[-1]] = value


# Routes
@app.route("/")
def index():
    """Main admin dashboard."""
    settings = load_settings()
    status = get_backend_status()
    return render_template(
        "index.html",
        settings=settings,
        status=status,
        schema=SETTINGS_SCHEMA
    )


@app.route("/settings")
def settings_page():
    """Settings management page."""
    settings = load_settings()
    return render_template(
        "settings.html",
        settings=settings,
        schema=SETTINGS_SCHEMA,
        groups=SETTINGS_GROUPS
    )


@app.route("/status")
def status_page():
    """Server status page."""
    status = get_backend_status()
    settings = load_settings()
    return render_template(
        "status.html",
        status=status,
        settings=settings
    )


# API Endpoints
@app.route("/api/settings", methods=["GET"])
def api_get_settings():
    """Get all settings."""
    return jsonify(load_settings())


@app.route("/api/settings", methods=["PUT"])
def api_put_settings():
    """Update all settings."""
    try:
        new_settings = request.json
        if save_settings(new_settings):
            return jsonify({"success": True, "settings": new_settings})
        return jsonify({"success": False, "error": "Failed to save settings"}), 500
    except Exception as e:
        return jsonify({"success": False, "error": str(e)}), 400


@app.route("/api/settings/<path:section>", methods=["GET"])
def api_get_section(section):
    """Get a specific settings section."""
    settings = load_settings()
    value = get_value_from_path(settings, section)
    if value is None:
        return jsonify({"error": "Section not found"}), 404
    return jsonify(value)


@app.route("/api/settings/<path:section>", methods=["PUT"])
def api_put_section(section):
    """Update a specific settings section."""
    try:
        settings = load_settings()
        set_value_at_path(settings, section, request.json)
        if save_settings(settings):
            return jsonify({"success": True})
        return jsonify({"success": False, "error": "Failed to save settings"}), 500
    except Exception as e:
        return jsonify({"success": False, "error": str(e)}), 400


@app.route("/api/status", methods=["GET"])
def api_get_status():
    """Get server status."""
    return jsonify(get_backend_status())


@app.route("/api/schema", methods=["GET"])
def api_get_schema():
    """Get settings schema for dynamic form generation."""
    return jsonify(SETTINGS_SCHEMA)


@app.route("/api/restart", methods=["POST"])
def api_restart_backend():
    """Signal to restart the backend (placeholder - needs implementation based on deployment)."""
    return jsonify({
        "success": False,
        "message": "Backend restart must be performed manually or via systemd/docker"
    })


@app.route("/api/proxy/health/<path:endpoint>", methods=["GET"])
def api_proxy_health(endpoint):
    """Proxy endpoint health checks to the backend to avoid CORS issues."""
    settings = load_settings()
    pin = settings.get("server", {}).get("pin", "")

    # Map endpoint names to actual paths
    endpoint_map = {
        "health": "/health",
        "settings": "/api/settings",
        "discover": "/api/discover/new",
        "users": "/api/users",
    }

    path = endpoint_map.get(endpoint, f"/{endpoint}")

    try:
        start_time = time.time()
        headers = {}
        # Health endpoint doesn't need auth, others do
        if endpoint != "health" and pin:
            headers["X-PIN"] = pin

        response = requests.get(
            f"http://{BACKEND_HOST}:{BACKEND_PORT}{path}",
            headers=headers,
            timeout=10
        )
        duration_ms = int((time.time() - start_time) * 1000)

        return jsonify({
            "status": response.status_code,
            "ok": response.ok,
            "duration_ms": duration_ms
        })
    except requests.exceptions.Timeout:
        return jsonify({
            "status": 0,
            "ok": False,
            "error": "timeout",
            "duration_ms": 10000
        })
    except requests.exceptions.RequestException as e:
        return jsonify({
            "status": 0,
            "ok": False,
            "error": str(e),
            "duration_ms": 0
        })


@app.route("/api/streams", methods=["GET"])
def api_get_streams():
    """Get active streams from the backend."""
    settings = load_settings()
    pin = settings.get("server", {}).get("pin", "")

    try:
        headers = {"X-PIN": pin} if pin else {}
        response = requests.get(
            f"http://{BACKEND_HOST}:{BACKEND_PORT}/api/admin/streams",
            headers=headers,
            timeout=5
        )
        if response.status_code == 200:
            return jsonify(response.json())
        return jsonify({"streams": [], "error": f"Backend returned {response.status_code}"})
    except requests.exceptions.RequestException as e:
        return jsonify({"streams": [], "error": str(e)})


if __name__ == "__main__":
    print(f"strmr Admin Panel starting on port {ADMIN_PORT}")
    print(f"Config path: {CONFIG_PATH}")
    print(f"Backend: http://{BACKEND_HOST}:{BACKEND_PORT}")

    # Run with hot reloading enabled for development
    app.run(
        host="0.0.0.0",
        port=ADMIN_PORT,
        debug=True,  # Enables hot reloading
        use_reloader=True,  # Watch for file changes
        threaded=True
    )
