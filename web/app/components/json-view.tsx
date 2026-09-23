export function JsonView({ value }: { value: unknown }) {
  return (
    <pre className="overflow-x-auto border border-line bg-sunken p-3 font-mono text-xs leading-5">
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}
