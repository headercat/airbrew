// Password / passphrase generator dialog.
//
// Replaces the single-shot generate button in the item editor with a modal
// that shows a live preview, lets the user pick the strength (random password
// vs. memorable passphrase), tweak the character classes or word count, and
// re-roll until satisfied before applying the result to the field.

import { useCallback, useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Modal } from "@/components/ui/modal";
import { Switch } from "@/components/ui/switch";
import {
  generatePassphrase,
  generatePasswordFromOptions,
  type PasswordOptions,
} from "@/lib/vault/crypto";

type Mode = "password" | "passphrase";

export function PasswordGeneratorDialog({
  open,
  onClose,
  onApply,
}: {
  open: boolean;
  onClose: () => void;
  onApply: (value: string) => void;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>("password");
  const [preview, setPreview] = useState("");

  // Password options
  const [length, setLength] = useState(20);
  const [upper, setUpper] = useState(true);
  const [lower, setLower] = useState(true);
  const [digits, setDigits] = useState(true);
  const [symbols, setSymbols] = useState(true);
  const [avoidAmbiguous, setAvoidAmbiguous] = useState(false);

  // Passphrase options
  const [words, setWords] = useState(5);
  const [separator, setSeparator] = useState("-");
  const [capitalize, setCapitalize] = useState(true);
  const [includeNumber, setIncludeNumber] = useState(true);

  const regenerate = useCallback(() => {
    if (mode === "password") {
      const opts: PasswordOptions = {
        length,
        upper,
        lower,
        digits,
        symbols,
        avoidAmbiguous,
      };
      setPreview(generatePasswordFromOptions(opts));
    } else {
      setPreview(
        generatePassphrase({ words, separator, capitalize, includeNumber }),
      );
    }
  }, [
    mode,
    length,
    upper,
    lower,
    digits,
    symbols,
    avoidAmbiguous,
    words,
    separator,
    capitalize,
    includeNumber,
  ]);

  // Regenerate on open and whenever an option changes.
  useEffect(() => {
    if (open) regenerate();
  }, [open, regenerate]);

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t("passwords.generator.title")}
      description={t("passwords.generator.description")}
    >
      <div className="space-y-4">
        {/* mode toggle */}
        <div className="flex gap-2">
          {(["password", "passphrase"] as Mode[]).map((m) => (
            <Button
              key={m}
              type="button"
              size="sm"
              variant={mode === m ? "default" : "outline"}
              onClick={() => setMode(m)}
            >
              {t(`passwords.generator.${m}`)}
            </Button>
          ))}
        </div>

        {/* preview */}
        <div className="flex items-center gap-2 rounded-md border bg-muted/40 px-3 py-2">
          <code className="flex-1 break-all font-mono text-sm">{preview}</code>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-8 w-8 shrink-0"
            onClick={regenerate}
            title={t("passwords.generator.regenerate")}
          >
            <RefreshCw className="h-4 w-4" />
          </Button>
        </div>

        {mode === "password" ? (
          <div className="space-y-3">
            <div className="grid gap-1">
              <Label htmlFor="pwlen">
                {t("passwords.generator.length")} ({length})
              </Label>
              <input
                id="pwlen"
                type="range"
                min={8}
                max={40}
                value={length}
                onChange={(e) => setLength(Number(e.target.value))}
                className="w-full"
              />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <ToggleRow
                label={t("passwords.generator.upper")}
                checked={upper}
                onChange={setUpper}
              />
              <ToggleRow
                label={t("passwords.generator.lower")}
                checked={lower}
                onChange={setLower}
              />
              <ToggleRow
                label={t("passwords.generator.digits")}
                checked={digits}
                onChange={setDigits}
              />
              <ToggleRow
                label={t("passwords.generator.symbols")}
                checked={symbols}
                onChange={setSymbols}
              />
              <ToggleRow
                label={t("passwords.generator.avoidAmbiguous")}
                checked={avoidAmbiguous}
                onChange={setAvoidAmbiguous}
              />
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="grid gap-1">
              <Label htmlFor="ppwords">
                {t("passwords.generator.words")} ({words})
              </Label>
              <input
                id="ppwords"
                type="range"
                min={3}
                max={8}
                value={words}
                onChange={(e) => setWords(Number(e.target.value))}
                className="w-full"
              />
            </div>
            <div className="grid gap-1">
              <Label htmlFor="ppsep">
                {t("passwords.generator.separator")}
              </Label>
              <Input
                id="ppsep"
                value={separator}
                onChange={(e) => setSeparator(e.target.value.slice(0, 3))}
              />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <ToggleRow
                label={t("passwords.generator.capitalize")}
                checked={capitalize}
                onChange={setCapitalize}
              />
              <ToggleRow
                label={t("passwords.generator.includeNumber")}
                checked={includeNumber}
                onChange={setIncludeNumber}
              />
            </div>
          </div>
        )}

        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            type="button"
            onClick={() => {
              onApply(preview);
              onClose();
            }}
          >
            {t("passwords.generator.apply")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function ToggleRow({
  label,
  checked,
  onChange,
}: {
  label: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-sm">
      <Switch checked={checked} onCheckedChange={onChange} />
      {label}
    </label>
  );
}
