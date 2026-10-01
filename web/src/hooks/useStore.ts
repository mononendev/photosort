import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { DEFAULT_LAYERS } from '../lib/pose';
import type { Layer } from '../lib/pose';

interface AppState {
  selected: string[];                      // paths selected in Browse (relative to photos root)
  toggleSelected: (p: string) => void;
  setSelected: (ps: string[]) => void;
  clearSelected: () => void;
  jobDefaults: { vlm: boolean; skip_tier0: boolean; rescan: boolean; revlm?: boolean; detector?: { model: string } | null };
  setJobDefaults: (d: Partial<AppState['jobDefaults']>) => void;
  layers: Layer[];                         // overlay layers shown in the photo view
  toggleLayer: (k: Layer) => void;
  overlaysHidden: boolean;                 // g hides every overlay layer at once; `layers` is kept for when they come back
  toggleOverlays: () => void;
  showMath: boolean;                       // the photo view's focus-math panel is open
  setShowMath: (v: boolean) => void;
  compareModel: string | null;             // another pose model to draw over each photo, or none
  setCompareModel: (m: string | null) => void;
}

const useStore = create<AppState>()(
  persist(
    (set) => ({
      selected: [],
      toggleSelected: (p) => set((s) => ({ selected: s.selected.includes(p) ? s.selected.filter((x) => x !== p) : [...s.selected, p] })),
      setSelected: (ps) => set({ selected: ps }),
      clearSelected: () => set({ selected: [] }),
      jobDefaults: { vlm: true, skip_tier0: false, rescan: false, revlm: false },
      setJobDefaults: (d) => set((s) => ({ jobDefaults: { ...s.jobDefaults, ...d } })),
      layers: DEFAULT_LAYERS,
      // While hidden, clicking a layer brings the overlays back with that layer on rather than toggling it unseen.
      toggleLayer: (k) => set((s) => s.overlaysHidden
        ? { overlaysHidden: false, layers: s.layers.includes(k) ? s.layers : [...s.layers, k] }
        : { layers: s.layers.includes(k) ? s.layers.filter((x) => x !== k) : [...s.layers, k] }),
      overlaysHidden: false,
      toggleOverlays: () => set((s) => ({ overlaysHidden: !s.overlaysHidden })),
      showMath: false,
      setShowMath: (v) => set({ showMath: v }),
      compareModel: null,
      setCompareModel: (m) => set({ compareModel: m }),
    }),
    { name: 'photosort_ui', partialize: (s) => ({ selected: s.selected, jobDefaults: s.jobDefaults, layers: s.layers, showMath: s.showMath }) },
  ),
);

export default useStore;
