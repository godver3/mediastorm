export interface GenreConfig {
  id: number;
  name: string;
  icon?: string;
  iconFamily?: 'Ionicons' | 'MaterialCommunityIcons';
  tintColor?: string;
}

export const MOVIE_GENRES: GenreConfig[] = [
  { id: 28, name: 'Action', icon: 'flash', tintColor: 'rgba(239,68,68,0.12)' },
  { id: 35, name: 'Comedy', icon: 'happy', tintColor: 'rgba(245,158,11,0.12)' },
  { id: 18, name: 'Drama', icon: 'theater-masks', iconFamily: 'MaterialCommunityIcons', tintColor: 'rgba(168,85,247,0.12)' },
  { id: 27, name: 'Horror', icon: 'skull', tintColor: 'rgba(185,28,28,0.12)' },
  { id: 878, name: 'Sci-Fi', icon: 'rocket', tintColor: 'rgba(59,130,246,0.12)' },
  { id: 10749, name: 'Romance', icon: 'heart', tintColor: 'rgba(236,72,153,0.12)' },
  { id: 53, name: 'Thriller', icon: 'eye', tintColor: 'rgba(249,115,22,0.12)' },
  { id: 16, name: 'Animation', icon: 'color-palette', tintColor: 'rgba(20,184,166,0.12)' },
  { id: 99, name: 'Documentary', icon: 'film', tintColor: 'rgba(100,116,139,0.12)' },
  { id: 14, name: 'Fantasy', icon: 'sparkles', tintColor: 'rgba(139,92,246,0.12)' },
  { id: 80, name: 'Crime', icon: 'shield', iconFamily: 'MaterialCommunityIcons', tintColor: 'rgba(75,85,99,0.12)' },
  { id: 12, name: 'Adventure', icon: 'compass', tintColor: 'rgba(34,197,94,0.12)' },
  { id: 10751, name: 'Family', icon: 'people', tintColor: 'rgba(251,146,60,0.12)' },
  { id: 36, name: 'History', icon: 'hourglass', tintColor: 'rgba(180,150,100,0.12)' },
  { id: 9648, name: 'Mystery', icon: 'search', tintColor: 'rgba(6,182,212,0.12)' },
  { id: 10402, name: 'Music', icon: 'musical-notes', tintColor: 'rgba(192,132,252,0.12)' },
  { id: 10752, name: 'War', icon: 'flag', iconFamily: 'MaterialCommunityIcons', tintColor: 'rgba(101,115,65,0.12)' },
  { id: 37, name: 'Western', icon: 'sunny', tintColor: 'rgba(217,180,105,0.12)' },
];

export const TV_GENRES: GenreConfig[] = [
  { id: 10759, name: 'Action & Adventure', icon: 'flash', tintColor: 'rgba(239,68,68,0.12)' },
  { id: 35, name: 'Comedy', icon: 'happy', tintColor: 'rgba(245,158,11,0.12)' },
  { id: 18, name: 'Drama', icon: 'theater-masks', iconFamily: 'MaterialCommunityIcons', tintColor: 'rgba(168,85,247,0.12)' },
  { id: 10765, name: 'Sci-Fi & Fantasy', icon: 'rocket', tintColor: 'rgba(59,130,246,0.12)' },
  { id: 80, name: 'Crime', icon: 'shield', iconFamily: 'MaterialCommunityIcons', tintColor: 'rgba(75,85,99,0.12)' },
  { id: 9648, name: 'Mystery', icon: 'search', tintColor: 'rgba(6,182,212,0.12)' },
  { id: 10768, name: 'War & Politics', icon: 'flag', iconFamily: 'MaterialCommunityIcons', tintColor: 'rgba(101,115,65,0.12)' },
  { id: 10764, name: 'Reality', icon: 'videocam', tintColor: 'rgba(249,115,22,0.12)' },
  { id: 16, name: 'Animation', icon: 'color-palette', tintColor: 'rgba(20,184,166,0.12)' },
  { id: 99, name: 'Documentary', icon: 'film', tintColor: 'rgba(100,116,139,0.12)' },
  { id: 10762, name: 'Kids', icon: 'balloon', iconFamily: 'MaterialCommunityIcons', tintColor: 'rgba(251,146,60,0.12)' },
  { id: 37, name: 'Western', icon: 'sunny', tintColor: 'rgba(217,180,105,0.12)' },
];
