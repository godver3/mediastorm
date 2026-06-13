#!/usr/bin/env python3
"""
Search for subtitles using subliminal.
Accepts JSON input and outputs JSON array of subtitle results.
"""
import sys
import json
import base64
import re
import urllib.parse
import urllib.request
from babelfish import Language
from subliminal import list_subtitles, region
from subliminal.video import Episode, Movie

# Configure cache
region.configure('dogpile.cache.memory')

SUBDL_API_URL = "https://api.subdl.com/api/v1/subtitles"
SUBDL_DOWNLOAD_BASE = "https://dl.subdl.com"
SUBSOURCE_API_URL = "https://api.subsource.net/api/v1"
SUBSOURCE_BASE_URL = "https://subsource.net"


def normalise_words(value):
    return [
        token for token in re.sub(r'[\W_]+', ' ', (value or '').lower()).split()
        if len(token) > 1
    ]


def release_similarity(target, candidate):
    target_tokens = set(normalise_words(target))
    candidate_tokens = set(normalise_words(candidate))
    if not target_tokens or not candidate_tokens:
        return 0

    score = (len(target_tokens & candidate_tokens) / len(target_tokens)) * 100
    important = {
        '480p', '720p', '1080p', '2160p', 'web', 'webdl', 'webrip', 'hdtv',
        'bluray', 'bdrip', 'brrip', 'x264', 'x265', 'h264', 'h265', 'hevc',
        'avc', 'aac', 'ac3', 'eac3', 'ddp5', 'dts', 'hdr', 'hdr10', 'dv',
    }
    score += sum(2 for token in target_tokens & candidate_tokens if token in important)
    return score


def episode_match_score(release, season, episode):
    if season is None or episode is None:
        return 0
    try:
        wanted_season = int(season)
        wanted_episode = int(episode)
    except (TypeError, ValueError):
        return 0

    text = release or ''
    checks = [
        re.compile(r'\bS(?P<season>\d{1,2})E(?P<episode>\d{1,3})\b', re.IGNORECASE),
        re.compile(r'\b(?P<season>\d{1,2})x(?P<episode>\d{1,3})\b', re.IGNORECASE),
    ]
    for pattern in checks:
        for match in pattern.finditer(text):
            found_season = int(match.group('season'))
            found_episode = int(match.group('episode'))
            if found_season == wanted_season and found_episode == wanted_episode:
                return 1000
            return -1000

    for pattern in (
        re.compile(r'\b(?:episode|ep)\s*\.?\s*(?P<episode>\d{1,3})\b', re.IGNORECASE),
        re.compile(r'\bE(?P<episode>\d{2,3})\b', re.IGNORECASE),
    ):
        for match in pattern.finditer(text):
            found_episode = int(match.group('episode'))
            if found_episode == wanted_episode:
                return 500
            return -500

    return 0


def sort_results(results, params):
    target = build_release_info(params) or params.get("title", "")

    def key(result):
        release = result.get("release") or ""
        return (
            episode_match_score(release, params.get("season"), params.get("episode")),
            release_similarity(target, release),
            int(result.get("downloads") or 0),
        )

    results.sort(key=key, reverse=True)


def encode_external_id(payload):
    raw = json.dumps(payload, separators=(',', ':')).encode('utf-8')
    return base64.urlsafe_b64encode(raw).decode('ascii').rstrip('=')


def http_json(url, timeout=12, headers=None):
    request_headers = {
        "Accept": "application/json",
        "User-Agent": "mediastorm/1.0",
    }
    if headers:
        request_headers.update(headers)
    req = urllib.request.Request(url, headers=request_headers)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode('utf-8', errors='replace'))


def subdl_language(language):
    lang_map = {
        'en': 'EN', 'eng': 'EN', 'es': 'ES', 'spa': 'ES', 'fr': 'FR', 'fra': 'FR',
        'de': 'DE', 'deu': 'DE', 'it': 'IT', 'ita': 'IT', 'pt': 'PT', 'por': 'PT',
        'nl': 'NL', 'nld': 'NL', 'pl': 'PL', 'pol': 'PL', 'ru': 'RU', 'rus': 'RU',
        'ja': 'JA', 'jpn': 'JA', 'ko': 'KO', 'kor': 'KO', 'zh': 'ZH', 'zho': 'ZH',
        'ar': 'AR', 'ara': 'AR', 'he': 'HE', 'heb': 'HE', 'sv': 'SV', 'swe': 'SV',
        'no': 'NO', 'nor': 'NO', 'da': 'DA', 'dan': 'DA', 'fi': 'FI', 'fin': 'FI',
        'tr': 'TR', 'tur': 'TR', 'el': 'EL', 'ell': 'EL', 'hu': 'HU', 'hun': 'HU',
        'cs': 'CS', 'ces': 'CS', 'ro': 'RO', 'ron': 'RO', 'th': 'TH', 'tha': 'TH',
        'vi': 'VI', 'vie': 'VI', 'hr': 'HR', 'hrv': 'HR', 'sr': 'SR', 'srp': 'SR',
        'bs': 'BS', 'bos': 'BS',
    }
    return lang_map.get((language or '').lower(), (language or 'en').upper())


def subsource_language(language):
    lang_map = {
        'en': 'english', 'eng': 'english', 'es': 'spanish', 'spa': 'spanish',
        'fr': 'french', 'fra': 'french', 'de': 'german', 'deu': 'german',
        'it': 'italian', 'ita': 'italian', 'pt': 'portuguese', 'por': 'portuguese',
        'nl': 'dutch', 'nld': 'dutch', 'pl': 'polish', 'pol': 'polish',
        'ru': 'russian', 'rus': 'russian', 'ja': 'japanese', 'jpn': 'japanese',
        'ko': 'korean', 'kor': 'korean', 'zh': 'chinese', 'zho': 'chinese',
        'ar': 'arabic', 'ara': 'arabic', 'he': 'hebrew', 'heb': 'hebrew',
        'sv': 'swedish', 'swe': 'swedish', 'no': 'norwegian', 'nor': 'norwegian',
        'da': 'danish', 'dan': 'danish', 'fi': 'finnish', 'fin': 'finnish',
        'tr': 'turkish', 'tur': 'turkish', 'el': 'greek', 'ell': 'greek',
        'hu': 'hungarian', 'hun': 'hungarian', 'cs': 'czech', 'ces': 'czech',
        'ro': 'romanian', 'ron': 'romanian', 'th': 'thai', 'tha': 'thai',
        'vi': 'vietnamese', 'vie': 'vietnamese', 'hr': 'croatian', 'hrv': 'croatian',
        'sr': 'serbian', 'srp': 'serbian', 'bs': 'bosnian', 'bos': 'bosnian',
    }
    return lang_map.get((language or '').lower(), language or 'english')


def search_subdl(params):
    api_key = (params.get("subdl_api_key") or "").strip()
    if not api_key:
        return []

    query = {
        "api_key": api_key,
        "languages": subdl_language(params.get("language", "en")),
        "subs_per_page": "30",
        "releases": "1",
        "hi": "1",
        "unpack": "1",
        "type": "tv" if params.get("season") is not None and params.get("episode") is not None else "movie",
    }
    imdb_id = params.get("imdb_id") or ""
    if imdb_id.startswith("tt"):
        query["imdb_id"] = imdb_id
    elif params.get("title"):
        query["film_name"] = params.get("title")
    if params.get("year"):
        query["year"] = str(params.get("year"))
    if params.get("season") is not None:
        query["season_number"] = str(params.get("season"))
    if params.get("episode") is not None:
        query["episode_number"] = str(params.get("episode"))

    data = http_json(f"{SUBDL_API_URL}?{urllib.parse.urlencode(query)}")
    if not data.get("status"):
        return []

    results = []
    for sub in data.get("subtitles") or []:
        candidates = sub.get("unpack_files") or [sub]
        for item in candidates:
            url = item.get("url") or sub.get("url")
            if not url:
                continue
            if url.startswith("/"):
                url = SUBDL_DOWNLOAD_BASE + url
            release = item.get("release_name") or sub.get("release_name") or item.get("name") or sub.get("name") or ""
            results.append({
                "id": encode_external_id({
                    "provider": "subdl",
                    "url": url,
                    "name": item.get("name") or sub.get("name") or release,
                    "season": params.get("season"),
                    "episode": params.get("episode"),
                }),
                "provider": "subdl",
                "language": item.get("language") or subdl_language(params.get("language", "en")),
                "release": release,
                "downloads": int(sub.get("downloads") or sub.get("download_count") or 0),
                "hearing_impaired": bool(item.get("hi") if "hi" in item else sub.get("hi", False)),
                "page_link": url,
            })
    return results


def search_subsource(params):
    api_key = (params.get("subsource_api_key") or "").strip()
    if not api_key:
        return []

    headers = {"X-API-Key": api_key}
    title = (params.get("title") or "").strip()
    imdb_id = (params.get("imdb_id") or "").strip()
    language = subsource_language(params.get("language", "en"))

    movie_ids = []
    if imdb_id.startswith("tt"):
        query = urllib.parse.urlencode({"searchType": "imdb", "imdb": imdb_id})
        data = http_json(f"{SUBSOURCE_API_URL}/movies/search?{query}", headers=headers)
        movie_ids.extend(str(item.get("movieId")) for item in data.get("data") or [] if item.get("movieId"))

    if not movie_ids and title:
        query = urllib.parse.urlencode({"searchType": "text", "q": title})
        data = http_json(f"{SUBSOURCE_API_URL}/movies/search?{query}", headers=headers)
        for item in data.get("data") or []:
            if params.get("year") and item.get("releaseYear") and int(item.get("releaseYear")) != int(params.get("year")):
                continue
            movie_id = item.get("movieId")
            if movie_id:
                movie_ids.append(str(movie_id))

    release_info = build_release_info(params)
    subtitles = []
    seen_subtitles = set()

    for movie_id in movie_ids[:3]:
        query = urllib.parse.urlencode({"movieId": movie_id, "language": language})
        data = http_json(f"{SUBSOURCE_API_URL}/subtitles?{query}", headers=headers)
        for sub in data.get("data") or []:
            subtitle_id = sub.get("subtitleId")
            if subtitle_id and subtitle_id not in seen_subtitles:
                seen_subtitles.add(subtitle_id)
                subtitles.append(sub)

    if release_info:
        query = urllib.parse.urlencode({"releaseInfo": release_info, "language": language})
        data = http_json(f"{SUBSOURCE_API_URL}/subtitles?{query}", headers=headers)
        for sub in data.get("data") or []:
            subtitle_id = sub.get("subtitleId")
            if subtitle_id and subtitle_id not in seen_subtitles:
                seen_subtitles.add(subtitle_id)
                subtitles.append(sub)

    results = []
    for sub in subtitles:
        subtitle_id = sub.get("subtitleId")
        if not subtitle_id:
            continue
        release = sub.get("releaseInfo") or []
        if isinstance(release, list):
            release = ", ".join(str(part) for part in release if str(part).strip())
        release = release or sub.get("commentary") or title
        link = sub.get("link") or ""
        page_link = urllib.parse.urljoin(SUBSOURCE_BASE_URL, link) if link else ""
        results.append({
            "id": encode_external_id({
                "provider": "subsource",
                "subtitle_id": str(subtitle_id),
                "name": release,
                "season": params.get("season"),
                "episode": params.get("episode"),
            }),
            "provider": "subsource",
            "language": sub.get("language") or language,
            "release": release,
            "downloads": int(sub.get("downloads") or 0),
            "hearing_impaired": bool(sub.get("hearingImpaired", False)),
            "page_link": page_link,
        })
    return results


def build_release_info(params):
    title = (params.get("title") or "").strip()
    if not title:
        return ""
    return " ".join(str(part) for part in [
        title,
        params.get("year") or "",
        f"S{int(params.get('season')):02d}E{int(params.get('episode')):02d}" if params.get("season") is not None and params.get("episode") is not None else "",
    ] if str(part).strip())


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

    results = []

    for provider_search in (search_subdl, search_subsource):
        try:
            results.extend(provider_search(params))
        except Exception as e:
            print(f"[subtitles] {provider_search.__name__} failed: {e}", file=sys.stderr)

    try:
        subtitles = list_subtitles([video], languages, providers=providers, provider_configs=provider_configs)
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
    except Exception as e:
        print(f"[subtitles] subliminal search failed: {e}", file=sys.stderr)

    sort_results(results, params)

    print(json.dumps(results))


if __name__ == "__main__":
    main()
