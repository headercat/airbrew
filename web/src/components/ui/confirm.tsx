import {
  createContext,
  useCallback,
  useContext,
  useRef,
  useState,
  type ReactNode,
} from "react";

import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";

// ConfirmDialog replaces jarring native window.confirm() calls with a themed,
// focus-trapped modal (the existing Modal primitive). useConfirm() returns an
// async confirm(opts) => Promise<boolean> so call sites keep the same control
// flow as the native function: `if (await confirm({...})) { ... }`.

type ConfirmOptions = {
  title: string;
  description?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  destructive?: boolean;
};

type ConfirmFn = (opts: ConfirmOptions) => Promise<boolean>;

const ConfirmContext = createContext<ConfirmFn | null>(null);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const [opts, setOpts] = useState<ConfirmOptions | null>(null);
  const resolveRef = useRef<((v: boolean) => void) | null>(null);

  const confirm = useCallback<ConfirmFn>((options) => {
    setOpts(options);
    setOpen(true);
    return new Promise<boolean>((resolve) => {
      resolveRef.current = resolve;
    });
  }, []);

  const close = useCallback((result: boolean) => {
    setOpen(false);
    resolveRef.current?.(result);
    resolveRef.current = null;
  }, []);

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Modal
        open={open}
        onClose={() => close(false)}
        title={opts?.title ?? ""}
        description={opts?.description}
      >
        <div className="mt-2 flex justify-end gap-2">
          <Button variant="ghost" onClick={() => close(false)}>
            {opts?.cancelLabel ?? "Cancel"}
          </Button>
          <Button
            variant={opts?.destructive ? "destructive" : "default"}
            onClick={() => close(true)}
            autoFocus
          >
            {opts?.confirmLabel ?? "Confirm"}
          </Button>
        </div>
      </Modal>
    </ConfirmContext.Provider>
  );
}

export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext);
  // Fallback to native confirm if the provider is missing so callers degrade
  // gracefully rather than crashing.
  if (!ctx) {
    return (opts) => Promise.resolve(window.confirm(opts.title));
  }
  return ctx;
}
