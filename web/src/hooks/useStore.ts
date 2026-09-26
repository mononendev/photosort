import { create } from 'zustand';
import { persist } from 'zustand/middleware';

interface AppState {
  selected: string[];                      // paths selected in Browse (relative to photos root)
  toggleSelected: (p: string) => void;
  setSelected: (ps: string[]) => void;
  clearSelected: () => void;
  jobDefaults: { vlm: boolean; skip_tier0: boolean; rescan: boolean };
  setJobDefaults: (d: Partial<AppState['jobDefaults']>) => void;
}

const useStore = create<AppState>()(
  persist(
    (set) => ({
      selected: [],
      toggleSelected: (p) => set((s) => ({ selected: s.selected.includes(p) ? s.selected.filter((x) => x !== p) : [...s.selected, p] })),
      setSelected: (ps) => set({ selected: ps }),
      clearSelected: () => set({ selected: [] }),
      jobDefaults: { vlm: true, skip_tier0: false, rescan: false },
      setJobDefaults: (d) => set((s) => ({ jobDefaults: { ...s.jobDefaults, ...d } })),
    }),
    { name: 'photosort_ui', partialize: (s) => ({ selected: s.selected, jobDefaults: s.jobDefaults }) },
  ),
);

export default useStore;
