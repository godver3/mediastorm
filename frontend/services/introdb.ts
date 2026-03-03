export interface IntroDBSegment {
  start_ms: number | null;
  end_ms: number | null;
  confidence: number;
  submission_count: number;
}

export interface IntroDBResponse {
  imdb_id: string;
  season: number;
  episode: number;
  intro: IntroDBSegment | null;
  recap: IntroDBSegment | null;
  outro: IntroDBSegment | null;
}

const INTRODB_BASE_URL = 'https://api.introdb.app';

export async function fetchSegments(
  imdbId: string,
  season: number,
  episode: number,
): Promise<IntroDBResponse | null> {
  try {
    const url = `${INTRODB_BASE_URL}/segments?imdb_id=${encodeURIComponent(imdbId)}&season=${season}&episode=${episode}`;
    const response = await fetch(url);
    if (!response.ok) {
      return null;
    }
    const data: IntroDBResponse = await response.json();
    return data;
  } catch {
    return null;
  }
}
