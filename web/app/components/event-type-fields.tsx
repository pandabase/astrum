import { eventGroups, eventTypes } from "~/lib/event-types";

/** Checkboxes for the events an endpoint receives; none checked means every event. */
export function EventTypeFields({ selected = [] }: { selected?: string[] }) {
  const chosen = new Set(selected);
  return (
    <fieldset className="grid gap-2">
      <legend className="mb-1 font-medium">Events</legend>
      <p className="text-muted">Leave all unchecked to receive every event.</p>
      <div className="grid grid-cols-2 gap-x-6 gap-y-1 md:grid-cols-3">
        {[...eventGroups, ...eventTypes].map((type) => (
          <label key={type} className="flex items-center gap-2 font-mono text-xs">
            <input type="checkbox" name="event_types" value={type} defaultChecked={chosen.has(type)} className="accent-ink" />
            {type}
          </label>
        ))}
      </div>
    </fieldset>
  );
}
