function focusNext(page, testInfo) {
  return page.keyboard.press(process.platform === 'darwin' && testInfo.project.use.browserName === 'webkit' ? 'Alt+Tab' : 'Tab');
}

module.exports = { focusNext };
