import { youTubeEmbedUrl, type Trailer } from '@/api/details';
import { Dialog } from '@/components';

/**
 * Plays a trailer in a dialog: YouTube trailers through the privacy-enhanced embed,
 * anything else as a plain <video>. (Native downloads YouTube trailers through
 * /api/metadata/trailers/prequeue because tvOS can't embed; a browser can.)
 */
export function TrailerDialog({
  trailer,
  open,
  onOpenChange,
}: {
  trailer?: Trailer;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const embed = trailer ? youTubeEmbedUrl(trailer) : undefined;
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={trailer?.name || 'Trailer'}
      size="lg"
      className="max-w-5xl!"
    >
      <div className="aspect-video w-full overflow-hidden rounded-xl bg-black">
        {open && trailer && embed && (
          <iframe
            src={embed}
            title={trailer.name || 'Trailer'}
            className="h-full w-full"
            allow="autoplay; encrypted-media; picture-in-picture; fullscreen"
            allowFullScreen
          />
        )}
        {open && trailer && !embed && (
          <video src={trailer.url} controls autoPlay className="h-full w-full" />
        )}
      </div>
    </Dialog>
  );
}
