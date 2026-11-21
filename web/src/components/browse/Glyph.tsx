import type { SVGProps } from 'react';

/** Extra icons for the browse pages (kept here so the shared icon set stays conflict-free). */
const paths = {
  plus: 'M12 5v14M5 12h14',
  check: 'M5 12.5l4.5 4.5L19 7',
  more: 'M5 12h.01M12 12h.01M19 12h.01',
  trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3',
  edit: 'M4 20h4L19 9l-4-4L4 16zM13.5 6.5l4 4',
  film: 'M4 4h16v16H4zM8 4v16M16 4v16M4 8h4M4 12h4M4 16h4M16 8h4M16 12h4M16 16h4',
  sortAsc: 'M7 4v16M3 16l4 4 4-4M14 6h7M14 12h5M14 18h3',
  sortDesc: 'M7 4v16M3 8l4-4 4 4M14 6h3M14 12h5M14 18h7',
  shuffle: 'M16 3h5v5M4 20 21 3M21 16v5h-5M15 15l6 6M4 4l5 5',
  folder: 'M3 6h6l2 2h10v11H3z',
  server: 'M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01',
} as const;

export type GlyphName = keyof typeof paths;

export function Glyph({
  name,
  size = 18,
  ...rest
}: { name: GlyphName; size?: number } & SVGProps<SVGSVGElement>) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={name === 'more' ? 3 : 1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...rest}
    >
      <path d={paths[name]} />
    </svg>
  );
}
