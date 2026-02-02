#!/usr/bin/env python3
"""
Download a subtitle using subliminal and convert SRT to VTT.
Accepts JSON input and outputs VTT content.
"""
import sys
import json
import re
from babelfish import Language
from subliminal import list_subtitles, download_subtitles, region
from subliminal.video import Episode, Movie

# Configure cache
region.configure('dogpile.cache.memory')


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

    if not subtitle_id or not provider:
        print(json.dumps({"error": "subtitle_id and provider are required"}), file=sys.stderr)
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
    supported_providers = ['podnapisi', 'opensubtitles']
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
        # Search for subtitles from the specific provider
        subtitles = list_subtitles([video], languages, providers=[provider], provider_configs=provider_configs)

        # Find the matching subtitle
        target_sub = None
        for sub in subtitles.get(video, []):
            sub_id = getattr(sub, 'subtitle_id', None) or getattr(sub, 'id', str(hash(sub)))
            if str(sub_id) == str(subtitle_id):
                target_sub = sub
                break

        if not target_sub:
            print(json.dumps({"error": f"Subtitle not found: {subtitle_id}"}), file=sys.stderr)
            sys.exit(1)

        # Download the subtitle
        download_subtitles([target_sub])

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
