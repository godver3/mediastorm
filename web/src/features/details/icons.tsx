import type { SVGProps } from 'react';

/**
 * Details-only icons, drawn like components/icons.tsx (24px grid, stroke =
 * currentColor). Kept here so parallel work on the shared set doesn't collide;
 * move into components/icons.tsx when convenient.
 */
const paths = {
  plus: 'M12 5v14M5 12h14',
  check: 'M5 12.5 10 17.5 19 7',
  eye: 'M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12zM12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z',
  eyeCheck: 'M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12zM9 12l2 2 4-4',
  film: 'M4 4h16v16H4zM8 4v16M16 4v16M4 8h4M4 12h4M4 16h4M16 8h4M16 12h4M16 16h4',
  restart: 'M3 12a9 9 0 1 0 3-6.7M3 4v5h5',
  drive: 'M3 13h18v6H3zM5 13l2-8h10l2 8M7 16h.01',
  sources: 'M4 6h16M4 12h10M4 18h6M18 14l3 3-3 3',
  star: 'M12 3l2.7 5.6 6.1.9-4.4 4.3 1 6.1L12 17l-5.4 2.9 1-6.1L3.2 9.5l6.1-.9z',
} as const;

export type DetailIconName = keyof typeof paths;

export function DetailIcon({
  name,
  size = 20,
  ...rest
}: { name: DetailIconName; size?: number } & SVGProps<SVGSVGElement>) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...rest}
    >
      <path d={paths[name]} />
    </svg>
  );
}
