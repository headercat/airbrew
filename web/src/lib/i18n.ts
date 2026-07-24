// i18next bootstrap. Imported once in main.tsx for its side effect.
import i18n from "i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import { initReactI18next } from "react-i18next";

import en from "@/locales/en.json";
import ko from "@/locales/ko.json";

export const SUPPORTED_LANGUAGES = [
  { code: "en", label: "English" },
  { code: "ko", label: "한국어" },
] as const;

export type LanguageCode = (typeof SUPPORTED_LANGUAGES)[number]["code"];

export const LANGUAGE_STORAGE_KEY = "airbrew-lang";

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
      ko: { translation: ko },
    },
    fallbackLng: "en",
    supportedLngs: ["en", "ko"],
    nonExplicitSupportedLngs: true,
    detection: {
      order: ["localStorage", "navigator"],
      lookupLocalStorage: LANGUAGE_STORAGE_KEY,
      caches: ["localStorage"],
    },
    interpolation: {
      // React already escapes; no need for i18next to.
      escapeValue: false,
    },
  });

// Keep <html lang> in sync so screen readers and browser features work.
i18n.on("languageChanged", (lng) => {
  if (typeof document !== "undefined") {
    document.documentElement.lang = lng.slice(0, 2);
  }
});

export default i18n;
