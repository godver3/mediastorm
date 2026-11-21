import clsx, { type ClassValue } from 'clsx';

/** className combinator. */
export const cn = (...values: ClassValue[]) => clsx(values);
