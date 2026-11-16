// 桌面工具箱 macOS 原生层（Objective-C，实现见 native_darwin.m）。
// 只包含声明：Go 侧的 //export 回调与这些声明在同一个 cgo 文件中。
#ifndef DT_NATIVE_H
#define DT_NATIVE_H

#include <stdint.h>

// 应用/窗口
void dtInstallReopenHandler(void);
void dtActivateApp(void);
void dtHideApp(void);
int dtIsAppActive(void);
int dtIsMainWindowVisible(void);
void dtAlert(const char *title, const char *msg);

// 全局热键（Carbon RegisterEventHotKey）。返回 OSStatus，0 表示成功。
int dtRegisterHotkey(int id, unsigned int keyCode, unsigned int mods);
void dtUnregisterHotkeys(void);

// 菜单栏图标
void dtTrayBegin(void);
void dtTrayAdd(int id, const char *label, const char *keyEquivalent, unsigned int keyMods);
void dtTrayCommit(const char *tooltip);
void dtTraySetTitle(const char *title);
void dtTrayRemove(void);

// 剪贴板
long dtClipChangeCount(void);
// 读取剪贴板：返回 0 无内容，1 文本（*text 需 free），2 图片 PNG（*png 需 free），3 应忽略（密码等）。
int dtClipRead(char **text, void **png, int *pngLen);
int dtClipSetText(const char *text);
int dtClipSetPNG(const void *data, int len);

// 权限
int dtScreenCaptureAllowed(void);
int dtRequestScreenCapture(void);

// 区域选择 / 屏幕信息。fixedW/fixedH 为像素（0 表示自由框选）。结果通过 dtGoRegion 回调。
void dtSelectRegion(int fixedW, int fixedH);
// 鼠标所在屏幕：写入点尺寸、缩放与 CGGetActiveDisplayList 中的序号。
void dtScreenUnderMouse(double *w, double *h, double *scale, int *displayIndex);

// 录屏悬浮条
void dtShowRecordingBar(double startedUnix);
void dtHideRecordingBar(void);

#endif
