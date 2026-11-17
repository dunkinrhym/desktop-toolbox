// 桌面工具箱 Linux（X11 + GTK3）原生层：托盘、剪贴板、全局热键、截屏、框选遮罩、录屏悬浮条。
// 所有 lnx_* GTK 函数必须在 GTK 主线程调用（Go 侧用 OnMain 调度）；热键函数使用独立的 X 连接，可在任意线程调用。
#ifndef DT_LNX_H
#define DT_LNX_H

#include <stdint.h>
#include <gtk/gtk.h>
#include <X11/Xlib.h>

// 遮罩动作
enum { OV_CANCEL = 0, OV_COPY = 1, OV_SAVE = 2, OV_DESKTOP = 3, OV_OCR = 4, OV_RECORD = 5 };
// 遮罩模式
enum { OVM_SHOT = 0, OVM_RECORD = 1, OVM_RECORD_FIXED = 2 };

void lnx_call_main(uintptr_t id);
int lnx_gtk_init(void);
void lnx_gtk_main(void);
void lnx_gtk_quit(void);

// 托盘（GtkStatusIcon，XEmbed 系统托盘，深度/统信 DDE 任务栏支持）
void lnx_tray_begin(const char *tooltip, const char *icon_path);
void lnx_tray_item(int id, const char *label);
void lnx_tray_end(void);
void lnx_tray_remove(void);
int lnx_tray_embedded(void);

// 剪贴板
void lnx_clip_watch(int on);
void lnx_clip_set_text(const char *text);
int lnx_clip_set_png(const unsigned char *data, int len);
char *lnx_clip_get_text(void);
int lnx_clip_get_png(unsigned char **out, int *len);
int lnx_clip_owned(void);

// 主窗口
int lnx_win_state(const char *title); // bit0 可见，bit1 处于激活状态
void lnx_win_present(const char *title);
void lnx_win_set_icon(const char *title, const char *icon_path);

// 截屏 / 屏幕信息
int lnx_capture_png(unsigned char **out, int *len, int *w, int *h);
void lnx_screen_info(int *pw, int *ph, double *scale);
void lnx_monitor_at_pointer(int *x, int *y, int *w, int *h);

// 框选遮罩（结果通过 goOverlayDone 回调）
int lnx_overlay_show(int mode, int fixw, int fixh, uint64_t token);
int lnx_overlay_active(void);

// 录屏悬浮条（区域为物理像素）
void lnx_recbar_show(int x, int y, int w, int h, int64_t start_ms);
void lnx_recbar_hide(void);

// 杂项
void lnx_alert(const char *title, const char *msg);
int lnx_show_uri(const char *uri, char **err);
void lnx_free(void *p);

// 测试窗口（自检截屏用：彩色渐变）
void lnx_test_window(int show);

// 全局热键（独立 X 连接）
Display *lnx_hk_open(void);
unsigned int lnx_hk_keycode(Display *d, unsigned long keysym);
int lnx_hk_grab(Display *d, unsigned int keycode, unsigned int mods, int grab);
int lnx_hk_poll(Display *d, int timeout_ms, unsigned int *keycode, unsigned int *state);

// XTest（自检 / 冒烟测试模拟按键与鼠标）
int lnx_xtest_keys(const unsigned long *keysyms, int n);
int lnx_xtest_pointer(int x, int y, int button, int press);

#endif
