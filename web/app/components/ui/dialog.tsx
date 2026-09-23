import { useEffect, useId, useRef, type ReactNode } from "react";

type DialogProps = {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
};

/** A modal built on the native dialog element, which handles focus trapping and Escape. */
export function Dialog({ open, onClose, title, children }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      onClose={onClose}
      aria-labelledby={titleId}
      className="m-auto w-full max-w-md border border-line-strong bg-canvas p-0 text-ink backdrop:bg-black/40"
    >
      <div className="grid gap-4 p-5">
        <h2 id={titleId} className="font-semibold">
          {title}
        </h2>
        {children}
      </div>
    </dialog>
  );
}
