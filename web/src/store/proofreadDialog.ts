import { create } from "zustand";

export type ProofreadTarget = {
  mediaId: number;
  title?: string;
};

type ProofreadDialogState = {
  subtitle: ProofreadTarget | null;
  lyric: ProofreadTarget | null;
  openSubtitle: (target: ProofreadTarget) => void;
  closeSubtitle: () => void;
  openLyric: (target: ProofreadTarget) => void;
  closeLyric: () => void;
};

export const useProofreadDialogStore = create<ProofreadDialogState>((set) => ({
  subtitle: null,
  lyric: null,
  openSubtitle: (target) => set({ subtitle: target }),
  closeSubtitle: () => set({ subtitle: null }),
  openLyric: (target) => set({ lyric: target }),
  closeLyric: () => set({ lyric: null }),
}));

export function openSubtitleProofreadDialog(mediaId: number, title?: string): void {
  useProofreadDialogStore.getState().openSubtitle({ mediaId, title });
}

export function openLyricProofreadDialog(mediaId: number, title?: string): void {
  useProofreadDialogStore.getState().openLyric({ mediaId, title });
}
