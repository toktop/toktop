import i18next from "i18next"
import LanguageDetector from "i18next-browser-languagedetector"
import { initReactI18next } from "react-i18next"

import en from "./locales/en.json"

void i18next
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources:      { en: { translation: en } },
    lng:            "en",
    fallbackLng:    "en",
    interpolation:  { escapeValue: false },
    detection: {
      order:  ["localStorage", "navigator"],
      caches: ["localStorage"],
    },
  })

i18next.on("languageChanged", (lng) => {
  document.documentElement.lang = lng
  document.documentElement.dir  = i18next.dir(lng)
})

// Set on init (i18next fires languageChanged after init resolves,
// but set eagerly so it's correct before the first render too).
document.documentElement.lang = i18next.language
document.documentElement.dir  = i18next.dir(i18next.language)

export default i18next
