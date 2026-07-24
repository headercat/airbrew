// Avoids the FOUC for dark mode by inlining a tiny script in index.html that
// sets the `dark` class before React hydrates. This module exports the script
// body so it can be embedded via dangerouslySetInnerHTML.
export const themeInitScript = `
(function() {
  try {
    var t = localStorage.getItem('airbrew-theme') || 'system';
    var dark = t === 'dark' || (t === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches);
    if (dark) document.documentElement.classList.add('dark');
  } catch (_) {}
})();
`;
