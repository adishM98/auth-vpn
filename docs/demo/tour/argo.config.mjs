import { defineConfig } from '@argo-video/cli';

export default defineConfig({
  baseURL: 'https://localhost:19100',   // stage dashboard from ../env.sh up
  demosDir: 'demos',
  outputDir: 'videos',
  tts: {
    defaultVoice: 'af_heart',
    defaultSpeed: 1.0,
  },
  video: {
    width: 1600,
    height: 900,
    fps: 30,
    browser: 'webkit',         // webkit > firefox > chromium for video quality on macOS
    // deviceScaleFactor: 2,   // 2x capture + lanczos downscale (known issue with webkit — enable after fix)
    cursorHighlight: true,
  },
  export: {
    preset: 'slow',            // slower = smaller file, higher quality
    crf: 16,                   // 16-28 range (lower = higher quality)
    transition: { type: 'fade-through-black', durationMs: 1200 },
    // audio: { loudnorm: true, music: 'assets/bg.mp3', musicVolume: 0.15 },
    // watermark: { src: 'assets/logo.png', position: 'bottom-right', opacity: 0.16 },
  },
  overlays: {
    autoBackground: true,      // auto-detect dark/light page for overlay contrast
    // defaultPlacement: 'top-right',  // default zone when cue omits placement
  },
});
