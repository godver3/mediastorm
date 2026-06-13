#!/usr/bin/env python3
"""
Download a subtitle using subliminal and convert SRT to VTT.
Accepts JSON input and outputs VTT content.
"""
import sys
import json
import re
import base64
import io
import urllib.parse
import urllib.request
import zipfile
from babelfish import Language
from subliminal import region
from subliminal.core import ProviderPool
from subliminal.video import Episode, Movie

# Configure cache
region.configure('dogpile.cache.memory')

SUBSOURCE_API_URL = "https://api.subsource.net/api/v1"


def decode_external_id(value):
    padding = '=' * (-len(value) % 4)
    raw = base64.urlsafe_b64decode((value + padding).encode('ascii'))
    return json.loads(raw.decode('utf-8'))


def fetch_url(url, api_key="", timeout=20):
    headers = {
        "Accept": "*/*",
        "User-Agent": "Mozilla/5.0 (compatible; mediastorm/1.0; +https://github.com/godver3/mediastorm)",
    }
    if api_key:
        headers["x-api-key"] = api_key
        parsed = urllib.parse.urlparse(url)
        query = urllib.parse.parse_qs(parsed.query)
        query.setdefault("api_key", [api_key])
        url = urllib.parse.urlunparse(parsed._replace(query=urllib.parse.urlencode(query, doseq=True)))
    req = urllib.request.Request(url, headers=headers)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read(), resp.headers.get("content-type", "")


def fetch_subsource_subtitle(subtitle_id, api_key, timeout=20):
    if not api_key:
        raise ValueError("SubSource requires an API key")
    if not subtitle_id:
        raise ValueError("invalid SubSource subtitle id")
    url = f"{SUBSOURCE_API_URL}/subtitles/{urllib.parse.quote(str(subtitle_id))}/download"
    req = urllib.request.Request(url, headers={
        "Accept": "*/*",
        "User-Agent": "mediastorm/1.0",
        "X-API-Key": api_key,
    })
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read(), resp.headers.get("content-type", "")


def pick_zip_subtitle(content, season=None, episode=None):
    priority_exts = ('.srt', '.vtt', '.ass', '.ssa')
    with zipfile.ZipFile(io.BytesIO(content)) as zf:
        names = [name for name in zf.namelist() if not name.endswith('/')]
        ranked = []
        for ext in priority_exts:
            for name in names:
                if name.lower().endswith(ext):
                    ranked.append((score_zip_subtitle_name(name, ext, season, episode), name))
        if ranked:
            ranked.sort(key=lambda item: item[0], reverse=True)
            return zf.read(ranked[0][1])
    return b""


def score_zip_subtitle_name(name, ext, season=None, episode=None):
    base = name.rsplit('/', 1)[-1].lower()
    score = {
        '.srt': 40,
        '.vtt': 30,
        '.ass': 20,
        '.ssa': 10,
    }.get(ext, 0)

    try:
        season_num = int(season) if season is not None else None
        episode_num = int(episode) if episode is not None else None
    except (TypeError, ValueError):
        season_num = None
        episode_num = None

    if episode_num is not None:
        episode_patterns = [
            rf"s{season_num:02d}e{episode_num:02d}" if season_num is not None else "",
            rf"s{season_num}e{episode_num:02d}" if season_num is not None else "",
            rf"{season_num}x{episode_num:02d}" if season_num is not None else "",
            rf"episode[\s._-]*{episode_num:02d}",
            rf"ep[\s._-]*{episode_num:02d}",
            rf"e{episode_num:02d}",
        ]
        if any(pattern and re.search(pattern, base) for pattern in episode_patterns):
            score += 1000
        elif re.search(rf"(?<!\d){episode_num:02d}(?!\d)", base):
            score += 200
    if season_num is not None and re.search(rf"s{season_num:02d}|season[\s._-]*{season_num}", base):
        score += 50
    return score


def ass_to_vtt(ass_content: str) -> str:
    """Convert ASS/SSA subtitle format to WebVTT format."""
    vtt_lines = ["WEBVTT", ""]

    in_events = False
    format_line = None

    for line in ass_content.split('\n'):
        line = line.strip()

        if line.lower() == '[events]':
            in_events = True
            continue
        elif line.startswith('[') and in_events:
            # New section, stop processing events
            break

        if in_events:
            if line.lower().startswith('format:'):
                format_line = line[7:].strip().split(',')
                format_line = [f.strip().lower() for f in format_line]
            elif line.lower().startswith('dialogue:'):
                if not format_line:
                    continue

                # Parse dialogue line
                parts = line[9:].split(',', len(format_line) - 1)
                if len(parts) < len(format_line):
                    continue

                dialogue = dict(zip(format_line, parts))

                start = dialogue.get('start', '')
                end = dialogue.get('end', '')
                text = dialogue.get('text', '')

                if not start or not end or not text:
                    continue

                # Convert ASS timestamp (H:MM:SS.cc) to VTT (HH:MM:SS.mmm)
                def convert_timestamp(ts):
                    # ASS format: H:MM:SS.cc (centiseconds)
                    match = re.match(r'(\d+):(\d{2}):(\d{2})\.(\d{2})', ts)
                    if match:
                        h, m, s, cs = match.groups()
                        return f"{int(h):02d}:{m}:{s}.{cs}0"
                    return ts

                vtt_start = convert_timestamp(start)
                vtt_end = convert_timestamp(end)

                # Preserve ASS styling tags - the frontend handles them for:
                # - Positioning: {\an1} to {\an9} (numpad alignment)
                # - Styling: {\i1}, {\b1}, {\u1} (italic, bold, underline)
                # - Colors: {\c&HBBGGRR&} (primary color)
                # The frontend will parse and render these appropriately.
                # Only convert \N to actual newlines for VTT format.
                text = text.replace('\\N', '\n').replace('\\n', '\n')

                if text.strip():
                    vtt_lines.append(f"{vtt_start} --> {vtt_end}")
                    vtt_lines.append(text.strip())
                    vtt_lines.append("")

    return '\n'.join(vtt_lines)


def srt_to_vtt(srt_content: str) -> str:
    """Convert SRT subtitle format to WebVTT format."""
    if not srt_content:
        return "WEBVTT\n\n"

    # Start with VTT header
    vtt_lines = ["WEBVTT", ""]

    # Split into subtitle blocks
    blocks = re.split(r'\n\n+', srt_content.strip())

    for block in blocks:
        lines = block.strip().split('\n')
        if len(lines) < 2:
            continue

        # Skip the subtitle number line (first line in SRT)
        # Find the timestamp line
        timestamp_line = None
        text_start = 0

        for i, line in enumerate(lines):
            # SRT timestamp format: 00:00:00,000 --> 00:00:00,000
            if '-->' in line and ',' in line:
                timestamp_line = line
                text_start = i + 1
                break

        if not timestamp_line:
            continue

        # Convert timestamp format (comma to dot for milliseconds)
        vtt_timestamp = timestamp_line.replace(',', '.')

        # Get text lines
        text_lines = lines[text_start:]
        if not text_lines:
            continue

        # Add to VTT
        vtt_lines.append(vtt_timestamp)
        vtt_lines.extend(text_lines)
        vtt_lines.append("")

    return '\n'.join(vtt_lines)


def convert_to_vtt(content: str) -> str:
    """Detect subtitle format and convert to WebVTT."""
    if not content:
        return "WEBVTT\n\n"

    # Detect ASS/SSA format
    if '[Script Info]' in content or '[V4+ Styles]' in content or '[Events]' in content:
        return ass_to_vtt(content)

    # Assume SRT format
    return srt_to_vtt(content)


def download_external_provider(provider, subtitle_id, subdl_api_key="", subsource_api_key=""):
    payload = decode_external_id(subtitle_id)
    if payload.get("provider") != provider:
        raise ValueError("invalid subtitle id")

    if provider == "subsource":
        content, content_type = fetch_subsource_subtitle(payload.get("subtitle_id"), subsource_api_key)
        filename = (payload.get("name") or "subsource.zip").lower()
    else:
        url = payload.get("url") or ""
        if not url:
            raise ValueError("invalid subtitle id")
        content, content_type = fetch_url(url, api_key=subdl_api_key)
        filename = (payload.get("name") or url).lower()
    if zipfile.is_zipfile(io.BytesIO(content)) or filename.endswith('.zip') or 'zip' in (content_type or '').lower():
        content = pick_zip_subtitle(content, season=payload.get("season"), episode=payload.get("episode"))
    if not content:
        raise ValueError("failed to download subtitle content")
    text = content.decode('utf-8-sig', errors='replace')
    if text.lstrip().startswith("WEBVTT"):
        return text
    return convert_to_vtt(text)


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
    subtitle_id = params.get("subtitle_id")
    provider = params.get("provider")

    # OpenSubtitles credentials (optional)
    os_username = params.get("opensubtitles_username", "")
    os_password = params.get("opensubtitles_password", "")
    subdl_api_key = params.get("subdl_api_key", "")
    subsource_api_key = params.get("subsource_api_key", "")

    if not subtitle_id or not provider:
        print(json.dumps({"error": "subtitle_id and provider are required"}), file=sys.stderr)
        sys.exit(1)

    if provider in ("subdl", "subsource"):
        try:
            print(download_external_provider(
                provider,
                subtitle_id,
                subdl_api_key=subdl_api_key,
                subsource_api_key=subsource_api_key,
            ))
            return
        except Exception as e:
            print(json.dumps({"error": str(e)}), file=sys.stderr)
            sys.exit(1)

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
    }
    lang_code = lang_map.get(language, language)
    try:
        lang = Language(lang_code)
    except Exception:
        lang = Language('eng')

    languages = {lang}

    # Build provider config
    provider_configs = {}

    # Validate provider - opensubtitles requires credentials
    supported_providers = ['podnapisi', 'opensubtitles', 'subdl', 'subsource']
    if provider not in supported_providers:
        print(json.dumps({"error": f"Provider '{provider}' not supported. Supported: {', '.join(supported_providers)}"}), file=sys.stderr)
        sys.exit(1)

    if provider == 'opensubtitles':
        if not os_username or not os_password:
            print(json.dumps({"error": "OpenSubtitles requires username and password"}), file=sys.stderr)
            sys.exit(1)
        provider_configs['opensubtitles'] = {
            'username': os_username,
            'password': os_password,
        }

    try:
        # Search AND download within a single ProviderPool so the provider stays
        # logged in for both steps. Using the module-level list_subtitles() then
        # download_subtitles() opens two separate pools, which logs in twice in
        # quick succession — OpenSubtitles.org's XML-RPC anti-flood rejects the
        # second login with Unauthorized, yielding empty content. One session, one
        # login. (This is why automatic subtitles — fetched in a single pool —
        # worked while manual downloads via this script failed.)
        with ProviderPool(providers=[provider], provider_configs=provider_configs) as pool:
            subtitles = pool.list_subtitles(video, languages)

            # Find the matching subtitle
            target_sub = None
            for sub in subtitles:
                sub_id = getattr(sub, 'subtitle_id', None) or getattr(sub, 'id', str(hash(sub)))
                if str(sub_id) == str(subtitle_id):
                    target_sub = sub
                    break

            if not target_sub:
                print(json.dumps({"error": f"Subtitle not found: {subtitle_id}"}), file=sys.stderr)
                sys.exit(1)

            # Download the subtitle using the same logged-in provider
            pool.download_subtitle(target_sub)

        # Get content
        content = target_sub.text or (target_sub.content.decode('utf-8', errors='replace') if target_sub.content else '')

        if not content:
            print(json.dumps({"error": "Failed to download subtitle content. The provider may require authentication."}), file=sys.stderr)
            sys.exit(1)

        # Convert to VTT
        vtt_content = convert_to_vtt(content)

        # Output raw VTT (not JSON)
        print(vtt_content)

    except Exception as e:
        print(json.dumps({"error": str(e)}), file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
