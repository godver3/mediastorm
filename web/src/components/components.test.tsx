import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { Button, EmptyState, ErrorState, LandscapeCard, PosterCard, Shelf } from '.';

describe('components', () => {
  it('Button shows a spinner and disables while loading', () => {
    render(<Button loading>Save</Button>);
    expect(screen.getByRole('button', { name: /save/i })).toBeDisabled();
    expect(screen.getByRole('status')).toBeInTheDocument();
  });

  it('PosterCard renders title, subtitle, progress and handles clicks', async () => {
    const onClick = vi.fn();
    render(<PosterCard title="Heat" subtitle="1995" progress={0.4} onClick={onClick} />);
    expect(screen.getByText('1995')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '40');
    await userEvent.click(screen.getByRole('button', { name: 'Heat' }));
    expect(onClick).toHaveBeenCalled();
  });

  it('LandscapeCard renders as a link with href', () => {
    render(<LandscapeCard title="Episode" href="/x" overlayText />);
    expect(screen.getByRole('link', { name: 'Episode' })).toHaveAttribute('href', '/x');
  });

  it('Shelf renders its title and children', () => {
    render(
      <Shelf title="Trending">
        <PosterCard title="A" />
      </Shelf>,
    );
    expect(screen.getByRole('heading', { name: 'Trending' })).toBeInTheDocument();
  });

  it('ErrorState shows the message and retries', async () => {
    const retry = vi.fn();
    render(<ErrorState error={new Error('boom')} onRetry={retry} />);
    expect(screen.getByText('boom')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /try again/i }));
    expect(retry).toHaveBeenCalled();
    render(<EmptyState title="Nothing here" />);
    expect(screen.getByText('Nothing here')).toBeInTheDocument();
  });
});
