// Audio player state (Pinia). The mini-player survives article navigation, so
// the "now playing" track lives in its own store. The <audio> element is owned
// by PlayerBar; this store only carries intent. Ported from papr's player.ts.

import { defineStore } from "pinia";
import { ref } from "vue";

export interface Track {
  articleId: number;
  title: string;
  feedTitle: string;
  src: string;
}

export const PLAYBACK_RATES = [0.75, 1, 1.25, 1.5, 2] as const;
const DEFAULT_RATE = 1;
const RATE_KEY = "player.rate";

function loadRate(): number {
  const stored = Number(localStorage.getItem(RATE_KEY));
  return (PLAYBACK_RATES as readonly number[]).includes(stored) ? stored : DEFAULT_RATE;
}

export const usePlayer = defineStore("player", () => {
  const track = ref<Track | null>(null);
  const playing = ref(false);
  const rate = ref(loadRate());

  function play(t: Track) {
    if (track.value && track.value.src === t.src) {
      playing.value = !playing.value;
    } else {
      track.value = t;
      playing.value = true;
    }
  }
  const setPlaying = (v: boolean) => (playing.value = v);
  const toggle = () => (playing.value = track.value ? !playing.value : false);
  function setRate(r: number) {
    const next = (PLAYBACK_RATES as readonly number[]).includes(r) ? r : DEFAULT_RATE;
    localStorage.setItem(RATE_KEY, String(next));
    rate.value = next;
  }
  const close = () => {
    track.value = null;
    playing.value = false;
  };

  return { track, playing, rate, play, setPlaying, toggle, setRate, close };
});
