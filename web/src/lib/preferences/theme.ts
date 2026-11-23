import { t } from "@/i18n";

export const THEME_MODE_OPTIONS = [
  { label: t("Light"), value: "light" },
  { label: t("Dark"), value: "dark" },
  { label: t("System"), value: "system" },
] as const;

export const THEME_MODE_VALUES = THEME_MODE_OPTIONS.map((o) => o.value);
export type ThemeMode = (typeof THEME_MODE_VALUES)[number];
export type ResolvedThemeMode = "light" | "dark";

// --- generated:themePresets:start ---

export const THEME_PRESET_OPTIONS = [
  {
    label: "Default",
    value: "default",
    primary: {
      light: "oklch(0.205 0 0)",
      dark: "oklch(0.922 0 0)",
    },
  },
  {
    label: "Brutalist",
    value: "brutalist",
    primary: {
      light: "oklch(0.6489 0.237 26.9728)",
      dark: "oklch(0.7044 0.1872 23.1858)",
    },
  },
  {
    label: "Catppuccin",
    value: "catppuccin",
    primary: {
      light: "#8839ef",
      dark: "#c6a0f6",
    },
  },
  {
    label: "Dracula",
    value: "dracula",
    primary: {
      light: "#7c3aed",
      dark: "#bd93f9",
    },
  },
  {
    label: "GitHub",
    value: "github",
    primary: {
      light: "#0969da",
      dark: "#4493f8",
    },
  },
  {
    label: "Material You",
    value: "material",
    primary: {
      light: "#6750a4",
      dark: "#d0bcff",
    },
  },
  {
    label: "Neon",
    value: "neon",
    primary: {
      light: "#00685f",
      dark: "#54eccd",
    },
  },
  {
    label: "Nord",
    value: "nord",
    primary: {
      light: "#5e81ac",
      dark: "#88c0d0",
    },
  },
  {
    label: "One Dark",
    value: "one-dark",
    primary: {
      light: "#4078f2",
      dark: "#61afef",
    },
  },
  {
    label: "Rosé Pine",
    value: "rose-pine",
    primary: {
      light: "#907aa9",
      dark: "#c4a7e7",
    },
  },
  {
    label: "Soft Pop",
    value: "soft-pop",
    primary: {
      light: "oklch(0.5106 0.2301 276.9656)",
      dark: "oklch(0.6801 0.1583 276.9349)",
    },
  },
  {
    label: "Solarized",
    value: "solarized",
    primary: {
      light: "#268bd2",
      dark: "#268bd2",
    },
  },
  {
    label: "Tangerine",
    value: "tangerine",
    primary: {
      light: "oklch(0.64 0.17 36.44)",
      dark: "oklch(0.64 0.17 36.44)",
    },
  },
  {
    label: "Tokyo Night",
    value: "tokyo-night",
    primary: {
      light: "#3760bf",
      dark: "#7aa2f7",
    },
  },
] as const;

export const THEME_PRESET_VALUES = THEME_PRESET_OPTIONS.map((p) => p.value);

export type ThemePreset = (typeof THEME_PRESET_OPTIONS)[number]["value"];

// --- generated:themePresets:end ---
