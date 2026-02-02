#!/usr/bin/env python3
"""
Search for subtitles using subliminal.
Accepts JSON input and outputs JSON array of subtitle results.
"""
import sys
import json
from babelfish import Language
from subliminal import list_subtitles, region
from subliminal.video import Episode, Movie

# Configure cache
region.configure('dogpile.cache.memory')


def main():
    # Read params from stdin to avoid exposing credentials in process listings
    try:
        input_data = sys.stdin.read()
        if not input_data:
            print(json.dumps({"error": "No input provided"}), file=sys.stderr)
            sys.exit(1)
        params = json.loads(input_data)
    except json.JSONDecodeError as e:
        print(json.dumps({"error": f"Invalid JSON: {e}"}), file=sys.stderr)
        sys.exit(1)

    imdb_id = params.get("imdb_id", "")
    title = params.get("title", "")
    year = params.get("year")
    season = params.get("season")
    episode = params.get("episode")
    language = params.get("language", "en")

    # OpenSubtitles credentials (optional)
    os_username = params.get("opensubtitles_username", "")
    os_password = params.get("opensubtitles_password", "")

    # Determine if this is a TV show or movie
    if season is not None and episode is not None:
        video = Episode(
            name=title,
            series=title,
            season=int(season),
            episodes=[int(episode)],  # subliminal expects a list of episode numbers
            year=int(year) if year else None,
            series_imdb_id=imdb_id if imdb_id and imdb_id.startswith("tt") else None,
        )
    else:
        video = Movie(
            name=title,
            title=title,
            year=int(year) if year else None,
            imdb_id=imdb_id if imdb_id and imdb_id.startswith("tt") else None,
        )

    # Parse language - babelfish uses 3-letter ISO 639-2 codes
    # Map common 2-letter codes to 3-letter codes
    lang_map = {
        'en': 'eng', 'es': 'spa', 'fr': 'fra', 'de': 'deu', 'it': 'ita',
        'pt': 'por', 'nl': 'nld', 'pl': 'pol', 'ru': 'rus', 'ja': 'jpn',
        'ko': 'kor', 'zh': 'zho', 'ar': 'ara', 'he': 'heb', 'sv': 'swe',
        'no': 'nor', 'da': 'dan', 'fi': 'fin', 'tr': 'tur', 'el': 'ell',
        'hu': 'hun', 'cs': 'ces', 'ro': 'ron', 'th': 'tha', 'vi': 'vie',
        'hr': 'hrv', 'sr': 'srp', 'bs': 'bos',
    }
    lang_code = lang_map.get(language, language)
    try:
        lang = Language(lang_code)
    except Exception:
        lang = Language('eng')

    languages = {lang}

    # Build provider list and config
    # podnapisi works without auth, opensubtitles (.org) requires auth
    providers = ['podnapisi']
    provider_configs = {}

    # Add OpenSubtitles.org if credentials are provided
    if os_username and os_password:
        providers.insert(0, 'opensubtitles')  # Prefer OpenSubtitles when available
        provider_configs['opensubtitles'] = {
            'username': os_username,
            'password': os_password,
        }

    try:
        subtitles = list_subtitles([video], languages, providers=providers, provider_configs=provider_configs)

        results = []
        for sub in subtitles.get(video, []):
            # Get release info from various possible attributes
            release = (
                getattr(sub, 'release_info', '') or
                getattr(sub, 'movie_release_name', '') or
                getattr(sub, 'filename', '') or
                (getattr(sub, 'releases', [''])[0] if hasattr(sub, 'releases') and sub.releases else '')
            )
            result = {
                "id": str(getattr(sub, 'subtitle_id', None) or getattr(sub, 'id', hash(sub))),
                "provider": sub.provider_name,
                "language": str(sub.language),
                "release": release,
                "downloads": getattr(sub, 'download_count', 0) or 0,
                "hearing_impaired": getattr(sub, 'hearing_impaired', False),
                "page_link": getattr(sub, 'page_link', ''),
            }
            results.append(result)

        # Sort by downloads descending
        results.sort(key=lambda x: x.get('downloads', 0), reverse=True)

        print(json.dumps(results))
    except Exception as e:
        print(json.dumps({"error": str(e)}), file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
