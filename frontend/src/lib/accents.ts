// Accent palettes (from the design prototype). App.vue writes these onto the
// document root as --accent / --accent-soft / --accent-ink per theme; the
// Appearance settings swatches render from this same map so the picker shows
// exactly what will be applied. Single source of truth — an earlier hardcoded
// hex copy of these colours in AppearanceSection had silently drifted from
// the values actually applied.

export interface AccentPalette {
  /** Light-theme accent / soft wash / ink-on-soft. */
  accent: string;
  soft: string;
  ink: string;
  /** Dark-theme variants of the same three roles. */
  dAccent: string;
  dSoft: string;
  dInk: string;
}

// Curated presets — gold first, same hue as the linen/charcoal surfaces
// (~78). Cool stops stay available. Gold is metal, not mustard: modest
// chroma so it sits on the warm-neutral floor instead of fighting it.
// Light: L≈0.55–0.60; dark: lift L to ≈0.78–0.82.
export const ACCENTS: Record<string, AccentPalette> = {
  gold: { accent: "oklch(0.57 0.105 78)", soft: "oklch(0.955 0.022 78)", ink: "oklch(0.45 0.09 78)", dAccent: "oklch(0.80 0.088 78)", dSoft: "oklch(0.27 0.035 78)", dInk: "oklch(0.88 0.055 78)" },
  azure: { accent: "oklch(0.58 0.17 256)", soft: "oklch(0.95 0.03 250)", ink: "oklch(0.48 0.15 256)", dAccent: "oklch(0.70 0.15 255)", dSoft: "oklch(0.36 0.085 256)", dInk: "oklch(0.82 0.11 256)" },
  cyan: { accent: "oklch(0.60 0.12 215)", soft: "oklch(0.95 0.04 210)", ink: "oklch(0.45 0.10 220)", dAccent: "oklch(0.78 0.12 210)", dSoft: "oklch(0.34 0.06 215)", dInk: "oklch(0.84 0.10 210)" },
  emerald: { accent: "oklch(0.56 0.13 152)", soft: "oklch(0.94 0.05 150)", ink: "oklch(0.43 0.11 152)", dAccent: "oklch(0.74 0.13 152)", dSoft: "oklch(0.32 0.06 152)", dInk: "oklch(0.82 0.10 152)" },
  indigo: { accent: "oklch(0.52 0.14 268)", soft: "oklch(0.94 0.04 270)", ink: "oklch(0.40 0.12 268)", dAccent: "oklch(0.74 0.13 270)", dSoft: "oklch(0.30 0.06 268)", dInk: "oklch(0.82 0.10 270)" },
  violet: { accent: "oklch(0.55 0.15 300)", soft: "oklch(0.94 0.04 300)", ink: "oklch(0.43 0.13 300)", dAccent: "oklch(0.73 0.13 300)", dSoft: "oklch(0.31 0.06 300)", dInk: "oklch(0.82 0.10 300)" },
  slate: { accent: "oklch(0.45 0.03 250)", soft: "oklch(0.93 0.012 250)", ink: "oklch(0.34 0.03 250)", dAccent: "oklch(0.80 0.02 250)", dSoft: "oklch(0.32 0.02 250)", dInk: "oklch(0.88 0.02 250)" },
};

/** Derive a full palette from one user-picked hex, applying the same recipe
 *  the presets follow. Uses CSS relative-color syntax (`oklch(from <hex> …)`,
 *  WebKit 16.4+ — well within the Wails WKWebView baseline), so the light
 *  accent is the hex verbatim and every other role is re-lit from it: pale
 *  soft wash, darker ink, and the three dark-theme variants lifted in L.
 *  Chroma is scaled rather than fixed so a muted pick stays muted. */
export function customPalette(hex: string): AccentPalette {
  const from = (l: string, cScale: number) =>
    `oklch(from ${hex} ${l} calc(c * ${cScale}) h)`;
  return {
    accent: hex,
    soft: from("0.95", 0.25),
    ink: from("0.44", 1),
    dAccent: from("0.74", 0.9),
    dSoft: from("0.33", 0.45),
    dInk: from("0.83", 0.7),
  };
}

/** Resolve an accent selection (a preset key or "custom" + its hex) to a
 *  palette. Unknown keys fall back to gold, mirroring the store's guard. */
export function resolveAccent(key: string, customHex: string): AccentPalette {
  if (key === "custom") return customPalette(customHex);
  return ACCENTS[key] ?? ACCENTS.gold;
}
