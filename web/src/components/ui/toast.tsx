import {
  createContext,
  useCallback,
  useContext,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { AlertCircle, CheckCircle2, Info, X } from "lucide-react";

import { cn } from "@/lib/utils";

// Toast is a minimal, dependency-free notification system: a provider holds a
// list of toasts in state and renders them in a fixed-position viewport via a
// portal. useToast() exposes imperative helpers (toast.success / .error / ...).
// Auto-dismisses after a duration; polite aria-live so screen readers announce.

type ToastVariant = "success" | "error" | "info";

type ToastItem = {
  id: number;
  variant: ToastVariant;
  title: string;
  description?: string;
  duration: number;
};

type ToastInput = {
  title: string;
  description?: string;
  duration?: number;
};

type ToastApi = {
  success: (t: ToastInput) => void;
  error: (t: ToastInput) => void;
  info: (t: ToastInput) => void;
};

const ToastContext = createContext<ToastApi | null>(null);

const variantStyles: Record<
  ToastVariant,
  { icon: ReactNode; ring: string }
> = {
  success: {
    icon: <CheckCircle2 className="h-5 w-5 text-emerald-500" />,
    ring: "border-emerald-500/30",
  },
  error: {
    icon: <AlertCircle className="h-5 w-5 text-destructive" />,
    ring: "border-destructive/30",
  },
  info: {
    icon: <Info className="h-5 w-5 text-primary" />,
    ring: "border-primary/30",
  },
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const nextID = useRef(1);

  const dismiss = useCallback((id: number) => {
    setItems((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (variant: ToastVariant, input: ToastInput) => {
      const id = nextID.current++;
      const item: ToastItem = {
        id,
        variant,
        title: input.title,
        description: input.description,
        duration: input.duration ?? 4500,
      };
      setItems((prev) => [...prev, item]);
      if (item.duration > 0) {
        window.setTimeout(() => dismiss(id), item.duration);
      }
    },
    [dismiss],
  );

  const api: ToastApi = {
    success: (t) => push("success", t),
    error: (t) => push("error", { ...t, duration: t.duration ?? 7000 }),
    info: (t) => push("info", t),
  };

  return (
    <ToastContext.Provider value={api}>
      {children}
      {createPortal(
        <div
          aria-live="polite"
          aria-atomic="false"
          className="pointer-events-none fixed inset-x-0 top-4 z-[100] flex flex-col items-center gap-2 px-4 sm:items-end sm:pr-6"
        >
          {items.map((item) => (
            <Toast key={item.id} item={item} onClose={() => dismiss(item.id)} />
          ))}
        </div>,
        document.body,
      )}
    </ToastContext.Provider>
  );
}

function Toast({ item, onClose }: { item: ToastItem; onClose: () => void }) {
  const style = variantStyles[item.variant];
  return (
    <div
      role="status"
      className={cn(
        "pointer-events-auto flex w-full max-w-sm items-start gap-3 rounded-lg border bg-popover p-3 text-popover-foreground shadow-lg",
        "animate-in fade-in slide-in-from-top-2",
        style.ring,
      )}
    >
      <span className="mt-0.5 shrink-0">{style.icon}</span>
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium leading-tight">{item.title}</p>
        {item.description && (
          <p className="mt-0.5 text-xs text-muted-foreground">
            {item.description}
          </p>
        )}
      </div>
      <button
        onClick={onClose}
        aria-label="Close"
        className="shrink-0 rounded p-0.5 text-muted-foreground hover:text-foreground"
      >
        <X className="h-4 w-4" />
      </button>
    </div>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) {
    // Fallback no-op so callers in unmounted/providerless contexts don't crash.
    const noop = () => {};
    return { success: noop, error: noop, info: noop };
  }
  return ctx;
}
