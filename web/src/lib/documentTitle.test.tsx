import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Page } from '@/components';
import { formatDocumentTitle } from './documentTitle';

describe('document title', () => {
  it('formats page titles with the app name', () => {
    expect(formatDocumentTitle('Live TV')).toBe('Live TV · MediaStorm');
    expect(formatDocumentTitle('')).toBe('MediaStorm');
  });

  it('Page sets the tab title from a string title or documentTitle', () => {
    const { rerender } = render(<Page title="Calendar">x</Page>);
    expect(document.title).toBe('Calendar · MediaStorm');
    rerender(<Page documentTitle="Home">x</Page>);
    expect(document.title).toBe('Home · MediaStorm');
  });
});
