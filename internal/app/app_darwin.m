//go:build darwin && !nogui

// A window around the panel: WKWebView under a transparent title bar, so the
// page's own chrome reaches the top of the window. Links that leave the
// panel open in the default browser, and the Edit menu makes copy and paste
// shortcuts work inside the web view.

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

@interface GCCCodeApp : NSObject <NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate>
@property(strong) NSWindow *window;
@property(strong) WKWebView *webView;
@property(copy) NSString *url;
@property(copy) NSString *title;
@property NSSize size;
@end

static BOOL isPanelURL(NSURL *target, NSURL *panel) {
	if (target == nil) return YES;
	NSString *host = target.host ?: @"";
	if (![host isEqualToString:panel.host ?: @""]) return NO;
	NSNumber *port = target.port ?: @80;
	NSNumber *panelPort = panel.port ?: @80;
	return [port isEqualToNumber:panelPort];
}

@implementation GCCCodeApp

- (NSMenu *)mainMenu {
	NSMenu *bar = [NSMenu new];

	NSMenuItem *appItem = [bar addItemWithTitle:@"" action:nil keyEquivalent:@""];
	NSMenu *app = [NSMenu new];
	[app addItemWithTitle:[@"关于 " stringByAppendingString:self.title] action:@selector(orderFrontStandardAboutPanel:) keyEquivalent:@""];
	[app addItem:[NSMenuItem separatorItem]];
	[app addItemWithTitle:[@"隐藏 " stringByAppendingString:self.title] action:@selector(hide:) keyEquivalent:@"h"];
	NSMenuItem *others = [app addItemWithTitle:@"隐藏其他" action:@selector(hideOtherApplications:) keyEquivalent:@"h"];
	others.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagOption;
	[app addItemWithTitle:@"显示全部" action:@selector(unhideAllApplications:) keyEquivalent:@""];
	[app addItem:[NSMenuItem separatorItem]];
	[app addItemWithTitle:[@"退出 " stringByAppendingString:self.title] action:@selector(terminate:) keyEquivalent:@"q"];
	appItem.submenu = app;

	NSMenuItem *editItem = [bar addItemWithTitle:@"编辑" action:nil keyEquivalent:@""];
	NSMenu *edit = [[NSMenu alloc] initWithTitle:@"编辑"];
	[edit addItemWithTitle:@"撤销" action:@selector(undo:) keyEquivalent:@"z"];
	[edit addItemWithTitle:@"重做" action:@selector(redo:) keyEquivalent:@"Z"];
	[edit addItem:[NSMenuItem separatorItem]];
	[edit addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
	[edit addItemWithTitle:@"拷贝" action:@selector(copy:) keyEquivalent:@"c"];
	[edit addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
	[edit addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
	editItem.submenu = edit;

	NSMenuItem *viewItem = [bar addItemWithTitle:@"显示" action:nil keyEquivalent:@""];
	NSMenu *view = [[NSMenu alloc] initWithTitle:@"显示"];
	[view addItemWithTitle:@"重新载入" action:@selector(reload:) keyEquivalent:@"r"];
	viewItem.submenu = view;

	NSMenuItem *windowItem = [bar addItemWithTitle:@"窗口" action:nil keyEquivalent:@""];
	NSMenu *window = [[NSMenu alloc] initWithTitle:@"窗口"];
	[window addItemWithTitle:@"最小化" action:@selector(performMiniaturize:) keyEquivalent:@"m"];
	[window addItemWithTitle:@"缩放" action:@selector(performZoom:) keyEquivalent:@""];
	[window addItem:[NSMenuItem separatorItem]];
	[window addItemWithTitle:@"关闭" action:@selector(performClose:) keyEquivalent:@"w"];
	windowItem.submenu = window;
	NSApp.windowsMenu = window;
	return bar;
}

- (void)reload:(id)sender {
	[self.webView reload];
}

- (void)applicationDidFinishLaunching:(NSNotification *)note {
	NSApp.mainMenu = [self mainMenu];

	NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
	NSWindow *w = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, self.size.width, self.size.height) styleMask:style backing:NSBackingStoreBuffered defer:NO];
	w.title = self.title;
	w.titleVisibility = NSWindowTitleHidden;
	w.titlebarAppearsTransparent = YES;
	w.minSize = NSMakeSize(900, 600);
	w.delegate = self;
	w.releasedWhenClosed = NO;
	// The same canvas colour the page paints, so the title bar reads as part of it.
	w.backgroundColor = [NSColor colorWithName:nil dynamicProvider:^NSColor *(NSAppearance *appearance) {
		NSAppearanceName best = [appearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
		if ([best isEqualToString:NSAppearanceNameDarkAqua]) {
			return [NSColor colorWithSRGBRed:0x1c / 255.0 green:0x1c / 255.0 blue:0x1f / 255.0 alpha:1];
		}
		return [NSColor colorWithSRGBRed:0xf4 / 255.0 green:0xf5 / 255.0 blue:0xf7 / 255.0 alpha:1];
	}];

	WKWebViewConfiguration *cfg = [WKWebViewConfiguration new];
	WKUserScript *mark = [[WKUserScript alloc] initWithSource:@"window.GCCCODE_APP = true; document.documentElement.classList.add('app');"
												injectionTime:WKUserScriptInjectionTimeAtDocumentStart forMainFrameOnly:YES];
	[cfg.userContentController addUserScript:mark];
	WKWebView *web = [[WKWebView alloc] initWithFrame:w.contentView.bounds configuration:cfg];
	web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	web.navigationDelegate = self;
	web.UIDelegate = self;
	[web setValue:@NO forKey:@"drawsBackground"];
	if (@available(macOS 13.3, *)) {
		web.inspectable = YES;
	}
	[w.contentView addSubview:web];
	[web loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:self.url]]];

	self.window = w;
	self.webView = web;
	if (![w setFrameUsingName:@"GCCCodeMain"]) {
		[w center];
	}
	[w setFrameAutosaveName:@"GCCCodeMain"];
	[w makeKeyAndOrderFront:nil];
	[NSApp activateIgnoringOtherApps:YES];
}

- (void)windowWillClose:(NSNotification *)notification {
	// Let [NSApp run] return so Go can exit normally.
	[NSApp stop:nil];
	NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0];
	[NSApp postEvent:wake atStart:YES];
}

// Links out of the panel belong to the default browser.
- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action decisionHandler:(void (^)(WKNavigationActionPolicy))decide {
	NSURL *target = action.request.URL;
	if (isPanelURL(target, [NSURL URLWithString:self.url])) {
		decide(WKNavigationActionPolicyAllow);
		return;
	}
	[[NSWorkspace sharedWorkspace] openURL:target];
	decide(WKNavigationActionPolicyCancel);
}

- (WKWebView *)webView:(WKWebView *)webView createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)features {
	if (action.request.URL != nil) {
		[[NSWorkspace sharedWorkspace] openURL:action.request.URL];
	}
	return nil;
}

- (void)webView:(WKWebView *)webView runJavaScriptAlertPanelWithMessage:(NSString *)message initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(void))done {
	NSAlert *alert = [NSAlert new];
	alert.messageText = message;
	[alert addButtonWithTitle:@"好"];
	[alert beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse r) { done(); }];
}

- (void)webView:(WKWebView *)webView runJavaScriptConfirmPanelWithMessage:(NSString *)message initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(BOOL))done {
	NSAlert *alert = [NSAlert new];
	alert.messageText = message;
	[alert addButtonWithTitle:@"好"];
	[alert addButtonWithTitle:@"取消"];
	[alert beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse r) { done(r == NSAlertFirstButtonReturn); }];
}

@end

int gcccode_app_run(const char *url, const char *title, int width, int height) {
	@autoreleasepool {
		NSApplication *app = [NSApplication sharedApplication];
		[app setActivationPolicy:NSApplicationActivationPolicyRegular];
		GCCCodeApp *delegate = [GCCCodeApp new];
		delegate.url = [NSString stringWithUTF8String:url];
		delegate.title = [NSString stringWithUTF8String:title];
		delegate.size = NSMakeSize(width, height);
		app.delegate = delegate;
		[app run];
		return 0;
	}
}
