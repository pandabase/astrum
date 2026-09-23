export function LoadingPage() {
  return (
    <div role="status" className="mx-auto grid max-w-[100rem] gap-8 p-5 md:p-8 lg:p-10">
      <span className="sr-only">Loading Astrum…</span>
      <div aria-hidden="true" className="grid gap-8 motion-safe:animate-pulse">
        <div className="grid gap-3 border-b border-line pb-6">
          <div className="h-6 w-36 bg-line" />
          <div className="h-4 w-56 max-w-full bg-sunken" />
        </div>
        <div className="border border-line">
          <div className="h-11 border-b border-line bg-sunken" />
          {Array.from({ length: 6 }, (_, index) => (
            <div key={index} className="flex h-16 items-center justify-between gap-6 border-b border-line px-4 last:border-b-0">
              <div className="h-3 w-1/3 bg-sunken" />
              <div className="h-3 w-1/5 bg-sunken" />
              <div className="h-3 w-12 bg-sunken" />
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
