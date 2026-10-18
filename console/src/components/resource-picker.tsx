import { useEffect, useState } from "react";
import { ChevronsUpDownIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Spinner } from "@/components/ui/spinner";
import { message } from "@/api/session";

export type Choice = { id: string; label: string; description?: string };

// A combobox whose options come from a server-side search.
export function ResourcePicker({
  value,
  valueLabel,
  placeholder,
  search,
  onSelect,
  disabled,
  id,
}: {
  value: string;
  valueLabel?: string;
  placeholder: string;
  search: (query: string, signal: AbortSignal) => Promise<Choice[]>;
  onSelect: (choice: Choice) => void;
  disabled?: boolean;
  id?: string;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [choices, setChoices] = useState<Choice[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [label, setLabel] = useState("");
  useEffect(() => {
    if (!open) return;
    const abort = new AbortController();
    const timer = setTimeout(() => {
      setBusy(true);
      search(
        query.trim(),
        AbortSignal.any([abort.signal, AbortSignal.timeout(15000)]),
      )
        .then((items) => {
          if (abort.signal.aborted) return;
          setChoices(items);
          setError("");
        })
        .catch((e) => !abort.signal.aborted && setError(message(e)))
        .finally(() => !abort.signal.aborted && setBusy(false));
    }, 200);
    return () => {
      clearTimeout(timer);
      abort.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, query]);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button
            id={id}
            variant="outline"
            role="combobox"
            aria-expanded={open}
            disabled={disabled}
            className="w-full justify-between font-normal"
          />
        }
      >
        <span className={value ? "truncate" : "truncate text-muted-foreground"}>
          {value ? valueLabel || label || value : placeholder}
        </span>
        <ChevronsUpDownIcon className="opacity-50" />
      </PopoverTrigger>
      <PopoverContent className="w-(--anchor-width) p-0" align="start">
        <Command shouldFilter={false}>
          <CommandInput
            value={query}
            onValueChange={setQuery}
            placeholder="搜索…"
          />
          <CommandList>
            {busy && (
              <div className="flex justify-center py-6">
                <Spinner />
              </div>
            )}
            {!busy && <CommandEmpty>{error || "没有匹配项"}</CommandEmpty>}
            {!busy && (
              <CommandGroup>
                {choices.map((choice) => (
                  <CommandItem
                    key={choice.id}
                    value={choice.id}
                    data-checked={choice.id === value}
                    onSelect={() => {
                      setLabel(choice.label);
                      onSelect(choice);
                      setOpen(false);
                    }}
                  >
                    <div className="grid min-w-0 gap-0.5">
                      <span className="truncate">{choice.label}</span>
                      {choice.description && (
                        <span className="truncate text-xs text-muted-foreground">
                          {choice.description}
                        </span>
                      )}
                    </div>
                  </CommandItem>
                ))}
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
