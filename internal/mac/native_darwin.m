//go:build darwin && cgo

// 桌面工具箱 macOS 原生层：全局热键（Carbon）、菜单栏图标、剪贴板、屏幕录制权限、
// 区域选择遮罩、录屏悬浮条。使用 ARC（见 mac_darwin.go 的 CFLAGS）。
// 所有 AppKit 操作都在主线程执行（Wails 在主线程运行 NSApplication）。

#import <Cocoa/Cocoa.h>
#import <Carbon/Carbon.h>
#import <CoreGraphics/CoreGraphics.h>
#import <objc/runtime.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include "native.h"
#include "_cgo_export.h"

static void dtMainSync(dispatch_block_t b) {
    if ([NSThread isMainThread]) {
        b();
    } else {
        dispatch_sync(dispatch_get_main_queue(), b);
    }
}

static void dtMainAsync(dispatch_block_t b) {
    dispatch_async(dispatch_get_main_queue(), b);
}

static NSString *dtStr(const char *s) {
    if (s == NULL) return @"";
    NSString *r = [NSString stringWithUTF8String:s];
    return r ? r : @"";
}

// ------------------------------------------------------------------ 应用 / 窗口

static BOOL dtReopenIMP(id self, SEL _cmd, NSApplication *app, BOOL hasVisibleWindows) {
    dtGoReopen();
    return NO; // 由 Go 侧负责显示窗口
}

void dtInstallReopenHandler(void) {
    dtMainAsync(^{
        id<NSApplicationDelegate> d = [NSApp delegate];
        if (d == nil) return;
        SEL sel = @selector(applicationShouldHandleReopen:hasVisibleWindows:);
        Class c = [(NSObject *)d class];
        if (![(NSObject *)d respondsToSelector:sel]) {
            class_addMethod(c, sel, (IMP)dtReopenIMP, "c@:@c");
        }
    });
}

void dtActivateApp(void) {
    dtMainAsync(^{
        [NSApp unhide:nil];
        [NSApp activateIgnoringOtherApps:YES];
    });
}

void dtHideApp(void) {
    dtMainAsync(^{
        [NSApp hide:nil];
    });
}

int dtIsAppActive(void) {
    __block int r = 0;
    dtMainSync(^{
        r = ([NSApp isActive] && ![NSApp isHidden]) ? 1 : 0;
    });
    return r;
}

int dtIsMainWindowVisible(void) {
    __block int r = 0;
    dtMainSync(^{
        Class wc = NSClassFromString(@"WailsWindow");
        for (NSWindow *w in [NSApp windows]) {
            if ((wc == nil || [w isKindOfClass:wc]) && [w isVisible] && ![w isMiniaturized] && ![NSApp isHidden]) {
                r = 1;
                break;
            }
        }
    });
    return r;
}

void dtAlert(const char *title, const char *msg) {
    NSString *t = dtStr(title), *m = dtStr(msg);
    dtMainAsync(^{
        NSAlert *a = [[NSAlert alloc] init];
        a.messageText = t;
        a.informativeText = m;
        a.alertStyle = NSAlertStyleWarning;
        [a addButtonWithTitle:@"好"];
        [NSApp activateIgnoringOtherApps:YES];
        [a runModal];
    });
}

// ------------------------------------------------------------------ 全局热键

#define DT_MAX_HOTKEYS 16
static EventHandlerRef dtHotkeyHandlerRef = NULL;
static EventHotKeyRef dtHotkeyRefs[DT_MAX_HOTKEYS];
static const OSType dtHotkeySignature = 'DTbx';

static OSStatus dtHotkeyHandler(EventHandlerCallRef next, EventRef ev, void *ud) {
    EventHotKeyID hk;
    if (GetEventParameter(ev, kEventParamDirectObject, typeEventHotKeyID, NULL, sizeof(hk), NULL, &hk) == noErr &&
        hk.signature == dtHotkeySignature) {
        dtGoHotkey((int)hk.id);
        return noErr;
    }
    return eventNotHandledErr;
}

int dtRegisterHotkey(int id, unsigned int keyCode, unsigned int mods) {
    if (id < 0 || id >= DT_MAX_HOTKEYS) return -50; // paramErr
    __block int result = 0;
    dtMainSync(^{
        if (dtHotkeyHandlerRef == NULL) {
            EventTypeSpec spec = {kEventClassKeyboard, kEventHotKeyPressed};
            OSStatus st = InstallApplicationEventHandler(&dtHotkeyHandler, 1, &spec, NULL, &dtHotkeyHandlerRef);
            if (st != noErr) {
                result = (int)st;
                return;
            }
        }
        if (dtHotkeyRefs[id] != NULL) {
            UnregisterEventHotKey(dtHotkeyRefs[id]);
            dtHotkeyRefs[id] = NULL;
        }
        EventHotKeyID hk = {dtHotkeySignature, (UInt32)id};
        EventHotKeyRef ref = NULL;
        // kEventHotKeyExclusive：若其他程序已独占注册同一组合键则返回错误，便于提示“被占用”
        OSStatus st = RegisterEventHotKey(keyCode, mods, hk, GetApplicationEventTarget(), kEventHotKeyExclusive, &ref);
        if (st == noErr) dtHotkeyRefs[id] = ref;
        result = (int)st;
    });
    return result;
}

void dtUnregisterHotkeys(void) {
    dtMainSync(^{
        for (int i = 0; i < DT_MAX_HOTKEYS; i++) {
            if (dtHotkeyRefs[i] != NULL) {
                UnregisterEventHotKey(dtHotkeyRefs[i]);
                dtHotkeyRefs[i] = NULL;
            }
        }
    });
}

// ------------------------------------------------------------------ 菜单栏图标

@interface DTMenuTarget : NSObject
- (void)clicked:(id)sender;
- (void)stopRecording:(id)sender;
@end

@implementation DTMenuTarget
- (void)clicked:(id)sender {
    dtGoTray((int)[(NSMenuItem *)sender tag]);
}
- (void)stopRecording:(id)sender {
    dtGoRecStop();
}
@end

static DTMenuTarget *dtTarget = nil;
static NSStatusItem *dtStatusItem = nil;
static NSMenu *dtPendingMenu = nil;

static DTMenuTarget *dtGetTarget(void) {
    if (dtTarget == nil) dtTarget = [[DTMenuTarget alloc] init];
    return dtTarget;
}

void dtTrayBegin(void) {
    dtMainAsync(^{
        dtPendingMenu = [[NSMenu alloc] initWithTitle:@""];
        dtPendingMenu.autoenablesItems = NO;
    });
}

void dtTrayAdd(int id, const char *label, const char *keyEquivalent, unsigned int keyMods) {
    NSString *l = dtStr(label), *k = dtStr(keyEquivalent);
    dtMainAsync(^{
        if (dtPendingMenu == nil) return;
        if (id == 0) {
            [dtPendingMenu addItem:[NSMenuItem separatorItem]];
            return;
        }
        NSMenuItem *it = [[NSMenuItem alloc] initWithTitle:l action:@selector(clicked:) keyEquivalent:k];
        it.keyEquivalentModifierMask = (NSEventModifierFlags)keyMods;
        it.target = dtGetTarget();
        it.tag = id;
        it.enabled = YES;
        [dtPendingMenu addItem:it];
    });
}

void dtTrayCommit(const char *tooltip) {
    NSString *tip = dtStr(tooltip);
    dtMainAsync(^{
        if (dtStatusItem == nil) {
            dtStatusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
            NSImage *img = nil;
            if (@available(macOS 11.0, *)) {
                img = [NSImage imageWithSystemSymbolName:@"wrench.and.screwdriver" accessibilityDescription:tip];
            }
            if (img != nil) {
                img.template = YES;
                dtStatusItem.button.image = img;
                dtStatusItem.button.imagePosition = NSImageLeft;
            } else {
                dtStatusItem.button.title = @"工具箱";
            }
        }
        dtStatusItem.button.toolTip = tip;
        if (dtPendingMenu != nil) dtStatusItem.menu = dtPendingMenu;
        dtPendingMenu = nil;
    });
}

void dtTraySetTitle(const char *title) {
    NSString *t = dtStr(title);
    dtMainAsync(^{
        if (dtStatusItem == nil) return;
        if (dtStatusItem.button.image != nil) {
            dtStatusItem.button.title = t;
        } else {
            dtStatusItem.button.title = t.length ? t : @"工具箱";
        }
    });
}

void dtTrayRemove(void) {
    dtMainSync(^{
        if (dtStatusItem != nil) {
            [[NSStatusBar systemStatusBar] removeStatusItem:dtStatusItem];
            dtStatusItem = nil;
        }
    });
}

// ------------------------------------------------------------------ 剪贴板

long dtClipChangeCount(void) {
    __block long n = 0;
    dtMainSync(^{
        n = (long)[[NSPasteboard generalPasteboard] changeCount];
    });
    return n;
}

static BOOL dtShouldIgnore(NSArray<NSPasteboardType> *types) {
    // http://nspasteboard.org 约定：密码管理器等标记的“不要记录”内容
    static NSArray *ignored = nil;
    if (ignored == nil) {
        ignored = @[ @"org.nspasteboard.ConcealedType", @"org.nspasteboard.TransientType",
                     @"org.nspasteboard.AutoGeneratedType", @"com.agilebits.onepassword",
                     @"com.typeit4me.clipping", @"de.petermaurer.TransientPasteboardType",
                     @"Pasteboard generator type" ];
    }
    for (NSString *t in types) {
        if ([ignored containsObject:t]) return YES;
    }
    return NO;
}

int dtClipRead(char **text, void **png, int *pngLen) {
    __block int kind = 0;
    *text = NULL;
    *png = NULL;
    *pngLen = 0;
    dtMainSync(^{
        @autoreleasepool {
            NSPasteboard *pb = [NSPasteboard generalPasteboard];
            NSArray<NSPasteboardType> *types = [pb types];
            if (types == nil || types.count == 0) return;
            if (dtShouldIgnore(types)) {
                kind = 3;
                return;
            }
            // 从访达复制文件时，剪贴板里是文件 URL + 文件名 + 图标，不记录
            if ([types containsObject:NSPasteboardTypeFileURL]) {
                kind = 3;
                return;
            }
            NSString *s = [pb stringForType:NSPasteboardTypeString];
            if (s != nil && s.length > 0) {
                const char *u = [s UTF8String];
                if (u != NULL) {
                    *text = strdup(u);
                    kind = 1;
                    return;
                }
            }
            NSData *d = [pb dataForType:NSPasteboardTypePNG];
            if (d == nil) {
                NSData *tiff = [pb dataForType:NSPasteboardTypeTIFF];
                if (tiff != nil) {
                    NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:tiff];
                    if (rep != nil) d = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
                }
            }
            if (d != nil && d.length > 0) {
                void *buf = malloc(d.length);
                if (buf != NULL) {
                    memcpy(buf, d.bytes, d.length);
                    *png = buf;
                    *pngLen = (int)d.length;
                    kind = 2;
                }
            }
        }
    });
    return kind;
}

int dtClipSetText(const char *text) {
    NSString *s = dtStr(text);
    __block int r = 0;
    dtMainSync(^{
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        r = [pb setString:s forType:NSPasteboardTypeString] ? 0 : 1;
    });
    return r;
}

int dtClipSetPNG(const void *data, int len) {
    NSData *pngData = [NSData dataWithBytes:data length:(NSUInteger)len];
    __block int r = 0;
    dtMainSync(^{
        @autoreleasepool {
            NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:pngData];
            if (rep == nil) {
                r = 1;
                return;
            }
            // 同时提供 PNG 与 TIFF：Word / Pages / 微信 / 邮件都能直接粘贴；TIFF 保留 Retina 的 DPI 信息
            NSData *tiff = [rep TIFFRepresentation];
            NSPasteboard *pb = [NSPasteboard generalPasteboard];
            [pb clearContents];
            [pb declareTypes:@[ NSPasteboardTypePNG, NSPasteboardTypeTIFF ] owner:nil];
            BOOL ok = [pb setData:pngData forType:NSPasteboardTypePNG];
            if (tiff != nil) ok = [pb setData:tiff forType:NSPasteboardTypeTIFF] || ok;
            r = ok ? 0 : 2;
        }
    });
    return r;
}

// ------------------------------------------------------------------ 权限

int dtScreenCaptureAllowed(void) {
    if (@available(macOS 10.15, *)) {
        return CGPreflightScreenCaptureAccess() ? 1 : 0;
    }
    return 1;
}

int dtRequestScreenCapture(void) {
    if (@available(macOS 10.15, *)) {
        return CGRequestScreenCaptureAccess() ? 1 : 0;
    }
    return 1;
}

// ------------------------------------------------------------------ 屏幕信息

static NSScreen *dtMouseScreen(void) {
    NSPoint p = [NSEvent mouseLocation];
    for (NSScreen *s in [NSScreen screens]) {
        if (NSMouseInRect(p, s.frame, NO)) return s;
    }
    return [NSScreen mainScreen];
}

static int dtDisplayIndexForScreen(NSScreen *s) {
    NSNumber *n = s.deviceDescription[@"NSScreenNumber"];
    if (n == nil) return 0;
    CGDirectDisplayID did = (CGDirectDisplayID)[n unsignedIntValue];
    CGDirectDisplayID ids[32];
    uint32_t cnt = 0;
    if (CGGetActiveDisplayList(32, ids, &cnt) != kCGErrorSuccess) return 0;
    for (uint32_t i = 0; i < cnt; i++) {
        if (ids[i] == did) return (int)i;
    }
    return 0;
}

void dtScreenUnderMouse(double *w, double *h, double *scale, int *displayIndex) {
    __block double bw = 0, bh = 0, bs = 1;
    __block int bi = 0;
    dtMainSync(^{
        NSScreen *s = dtMouseScreen();
        bw = s.frame.size.width;
        bh = s.frame.size.height;
        bs = s.backingScaleFactor;
        bi = dtDisplayIndexForScreen(s);
    });
    *w = bw;
    *h = bh;
    *scale = bs;
    *displayIndex = bi;
}

// ------------------------------------------------------------------ 区域选择遮罩

@interface DTOverlayWindow : NSWindow
@end
@implementation DTOverlayWindow
- (BOOL)canBecomeKeyWindow { return YES; }
- (BOOL)canBecomeMainWindow { return YES; }
@end

@interface DTOverlayView : NSView
@property (nonatomic) NSPoint startPt;
@property (nonatomic) NSRect sel;
@property (nonatomic) BOOL dragging;
@property (nonatomic) BOOL hasSel;
@property (nonatomic) NSSize fixedPts; // 固定大小模式（点）；0 表示自由框选
@property (nonatomic) CGFloat scale;
@end

static DTOverlayWindow *dtOverlay = nil;
static NSScreen *dtOverlayScreen = nil;

static void dtFinishRegion(BOOL ok, NSRect r) {
    if (dtOverlay == nil) return;
    NSScreen *s = dtOverlayScreen;
    double sw = s.frame.size.width, sh = s.frame.size.height, sc = s.backingScaleFactor;
    int idx = dtDisplayIndexForScreen(s);
    [dtOverlay orderOut:nil];
    dtOverlay = nil;
    dtOverlayScreen = nil;
    dtGoRegion(ok ? 1 : 0, r.origin.x, r.origin.y, r.size.width, r.size.height, sw, sh, sc, idx);
}

@implementation DTOverlayView
- (BOOL)isFlipped { return YES; }
- (BOOL)acceptsFirstResponder { return YES; }
- (BOOL)acceptsFirstMouse:(NSEvent *)e { return YES; }
- (void)resetCursorRects {
    [self addCursorRect:self.bounds cursor:[NSCursor crosshairCursor]];
}

- (NSRect)fixedRectAt:(NSPoint)p {
    NSSize f = self.fixedPts;
    NSRect b = self.bounds;
    CGFloat x = p.x - f.width / 2, y = p.y - f.height / 2;
    if (x < 0) x = 0;
    if (y < 0) y = 0;
    if (x + f.width > b.size.width) x = b.size.width - f.width;
    if (y + f.height > b.size.height) y = b.size.height - f.height;
    return NSMakeRect(round(x), round(y), f.width, f.height);
}

- (NSPoint)pointOf:(NSEvent *)e {
    return [self convertPoint:e.locationInWindow fromView:nil];
}

- (void)mouseMoved:(NSEvent *)e {
    if (self.fixedPts.width > 0) {
        self.sel = [self fixedRectAt:[self pointOf:e]];
        self.hasSel = YES;
        [self setNeedsDisplay:YES];
    }
}

- (void)mouseDown:(NSEvent *)e {
    NSPoint p = [self pointOf:e];
    if (self.fixedPts.width > 0) {
        dtFinishRegion(YES, [self fixedRectAt:p]);
        return;
    }
    self.startPt = p;
    self.dragging = YES;
    self.hasSel = NO;
    [self setNeedsDisplay:YES];
}

- (void)mouseDragged:(NSEvent *)e {
    if (!self.dragging) return;
    NSPoint p = [self pointOf:e];
    NSRect b = self.bounds;
    p.x = fmax(0, fmin(p.x, b.size.width));
    p.y = fmax(0, fmin(p.y, b.size.height));
    NSPoint s = self.startPt;
    self.sel = NSMakeRect(fmin(s.x, p.x), fmin(s.y, p.y), fabs(p.x - s.x), fabs(p.y - s.y));
    self.hasSel = YES;
    [self setNeedsDisplay:YES];
}

- (void)mouseUp:(NSEvent *)e {
    if (!self.dragging) return;
    self.dragging = NO;
    NSRect r = self.sel;
    if (!self.hasSel || r.size.width < 8 || r.size.height < 8) {
        r = self.bounds; // 单击：整个屏幕
    }
    dtFinishRegion(YES, NSIntegralRect(r));
}

- (void)rightMouseDown:(NSEvent *)e {
    dtFinishRegion(NO, NSZeroRect);
}

- (void)keyDown:(NSEvent *)e {
    if (e.keyCode == 53) { // Esc
        dtFinishRegion(NO, NSZeroRect);
    } else if ((e.keyCode == 36 || e.keyCode == 76) && self.hasSel) { // Return / Enter
        dtFinishRegion(YES, NSIntegralRect(self.sel));
    }
}

- (void)cancelOperation:(id)sender {
    dtFinishRegion(NO, NSZeroRect);
}

static void dtDrawLabel(NSString *text, NSPoint at, CGFloat fontSize) {
    NSDictionary *attrs = @{
        NSFontAttributeName : [NSFont systemFontOfSize:fontSize weight:NSFontWeightMedium],
        NSForegroundColorAttributeName : [NSColor whiteColor]
    };
    NSSize sz = [text sizeWithAttributes:attrs];
    NSRect bg = NSMakeRect(at.x, at.y, sz.width + 16, sz.height + 8);
    [[NSColor colorWithCalibratedWhite:0 alpha:0.72] setFill];
    [[NSBezierPath bezierPathWithRoundedRect:bg xRadius:6 yRadius:6] fill];
    [text drawAtPoint:NSMakePoint(at.x + 8, at.y + 4) withAttributes:attrs];
}

- (void)drawRect:(NSRect)dirty {
    NSRect b = self.bounds;
    [[NSColor colorWithCalibratedWhite:0 alpha:0.38] setFill];
    NSRectFill(b);
    NSString *hint = self.fixedPts.width > 0
        ? [NSString stringWithFormat:@"移动鼠标放置 %d×%d 录制框，单击开始录制 · Esc 取消",
                                     (int)lround(self.fixedPts.width * self.scale), (int)lround(self.fixedPts.height * self.scale)]
        : @"拖动鼠标框选录制区域，松开即开始录制 · 单击录制整个屏幕 · Esc 取消";
    NSDictionary *ha = @{NSFontAttributeName : [NSFont systemFontOfSize:15 weight:NSFontWeightMedium]};
    NSSize hs = [hint sizeWithAttributes:ha];
    dtDrawLabel(hint, NSMakePoint(round((b.size.width - hs.width) / 2 - 8), 48), 15);
    if (self.hasSel) {
        NSRect r = self.sel;
        [[NSColor clearColor] setFill];
        NSRectFillUsingOperation(r, NSCompositingOperationCopy);
        [[NSColor colorWithCalibratedRed:1.0 green:0.23 blue:0.19 alpha:1.0] setStroke];
        NSBezierPath *bp = [NSBezierPath bezierPathWithRect:NSInsetRect(r, -1, -1)];
        bp.lineWidth = 2;
        [bp stroke];
        NSString *size = [NSString stringWithFormat:@"%d × %d", (int)lround(r.size.width * self.scale), (int)lround(r.size.height * self.scale)];
        CGFloat ly = r.origin.y - 30;
        if (ly < 4) ly = r.origin.y + 6;
        dtDrawLabel(size, NSMakePoint(r.origin.x, ly), 12);
    }
}
@end

void dtSelectRegion(int fixedW, int fixedH) {
    dtMainAsync(^{
        if (dtOverlay != nil) {
            dtFinishRegion(NO, NSZeroRect);
        }
        NSScreen *s = dtMouseScreen();
        CGFloat scale = s.backingScaleFactor > 0 ? s.backingScaleFactor : 1;
        NSRect frame = s.frame;
        DTOverlayWindow *w = [[DTOverlayWindow alloc] initWithContentRect:frame
                                                                styleMask:NSWindowStyleMaskBorderless
                                                                  backing:NSBackingStoreBuffered
                                                                    defer:NO];
        w.releasedWhenClosed = NO;
        w.opaque = NO;
        w.backgroundColor = [NSColor clearColor];
        w.hasShadow = NO;
        w.level = NSScreenSaverWindowLevel;
        w.acceptsMouseMovedEvents = YES;
        w.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorFullScreenAuxiliary |
                               NSWindowCollectionBehaviorStationary;
        DTOverlayView *v = [[DTOverlayView alloc] initWithFrame:NSMakeRect(0, 0, frame.size.width, frame.size.height)];
        v.scale = scale;
        if (fixedW > 0 && fixedH > 0) {
            CGFloat fw = fixedW / scale, fh = fixedH / scale;
            if (fw > frame.size.width) fw = frame.size.width;
            if (fh > frame.size.height) fh = frame.size.height;
            v.fixedPts = NSMakeSize(fw, fh);
            NSPoint m = [NSEvent mouseLocation];
            NSPoint local = NSMakePoint(m.x - frame.origin.x, frame.size.height - (m.y - frame.origin.y));
            v.sel = [v fixedRectAt:local];
            v.hasSel = YES;
        }
        w.contentView = v;
        dtOverlay = w;
        dtOverlayScreen = s;
        [NSApp activateIgnoringOtherApps:YES];
        [w setFrame:frame display:YES];
        [w makeKeyAndOrderFront:nil];
        [w makeFirstResponder:v];
        [w invalidateCursorRectsForView:v];
    });
}

// ------------------------------------------------------------------ 录屏悬浮条

static NSPanel *dtRecBar = nil;
static NSTimer *dtRecTimer = nil;
static NSTextField *dtRecLabel = nil;

static NSString *dtFormatElapsed(double started) {
    long s = (long)([[NSDate date] timeIntervalSince1970] - started);
    if (s < 0) s = 0;
    return [NSString stringWithFormat:@"%02ld:%02ld:%02ld", s / 3600, (s / 60) % 60, s % 60];
}

void dtShowRecordingBar(double startedUnix) {
    dtMainAsync(^{
        if (dtRecBar == nil) {
            NSRect fr = NSMakeRect(0, 0, 210, 40);
            NSPanel *p = [[NSPanel alloc] initWithContentRect:fr
                                                    styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
                                                      backing:NSBackingStoreBuffered
                                                        defer:NO];
            p.releasedWhenClosed = NO;
            p.opaque = NO;
            p.backgroundColor = [NSColor clearColor];
            p.hasShadow = YES;
            p.level = NSStatusWindowLevel;
            p.hidesOnDeactivate = NO;
            p.canHide = NO;
            p.movableByWindowBackground = YES;
            p.sharingType = NSWindowSharingNone; // 不出现在录屏画面中
            p.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorFullScreenAuxiliary |
                                   NSWindowCollectionBehaviorStationary;
            NSView *bg = [[NSView alloc] initWithFrame:fr];
            bg.wantsLayer = YES;
            bg.layer.backgroundColor = [[NSColor colorWithCalibratedWhite:0.1 alpha:0.88] CGColor];
            bg.layer.cornerRadius = 10;
            NSTextField *lbl = [NSTextField labelWithString:@"● 00:00:00"];
            lbl.font = [NSFont monospacedDigitSystemFontOfSize:14 weight:NSFontWeightMedium];
            lbl.textColor = [NSColor colorWithCalibratedRed:1.0 green:0.35 blue:0.32 alpha:1.0];
            lbl.frame = NSMakeRect(12, 10, 100, 20);
            [bg addSubview:lbl];
            NSButton *btn = [NSButton buttonWithTitle:@"停止录制" target:dtGetTarget() action:@selector(stopRecording:)];
            btn.bezelStyle = NSBezelStyleRounded;
            btn.frame = NSMakeRect(112, 4, 92, 32);
            [bg addSubview:btn];
            p.contentView = bg;
            dtRecBar = p;
            dtRecLabel = lbl;
        }
        NSScreen *s = dtMouseScreen();
        NSRect vf = s.visibleFrame;
        NSRect fr = dtRecBar.frame;
        [dtRecBar setFrameOrigin:NSMakePoint(round(NSMidX(vf) - fr.size.width / 2), NSMaxY(vf) - fr.size.height - 8)];
        [dtRecBar orderFrontRegardless];
        [dtRecTimer invalidate];
        dtRecLabel.stringValue = [@"● " stringByAppendingString:dtFormatElapsed(startedUnix)];
        dtRecTimer = [NSTimer scheduledTimerWithTimeInterval:0.5
                                                     repeats:YES
                                                       block:^(NSTimer *t) {
                                                           NSString *e = dtFormatElapsed(startedUnix);
                                                           dtRecLabel.stringValue = [@"● " stringByAppendingString:e];
                                                           if (dtStatusItem != nil) dtStatusItem.button.title = [@" ● " stringByAppendingString:e];
                                                       }];
        [[NSRunLoop mainRunLoop] addTimer:dtRecTimer forMode:NSRunLoopCommonModes];
    });
}

void dtHideRecordingBar(void) {
    dtMainAsync(^{
        [dtRecTimer invalidate];
        dtRecTimer = nil;
        if (dtRecBar != nil) [dtRecBar orderOut:nil];
        if (dtStatusItem != nil) dtStatusItem.button.title = dtStatusItem.button.image != nil ? @"" : @"工具箱";
    });
}
