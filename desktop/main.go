package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/KeepS13/HQIP/internal/winclient"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

type Desktop struct { controller *client.Controller }
func(d *Desktop)State()client.View{return d.controller.View()}
func(d *Desktop)Login(server,user,password string)error{return d.controller.Login(server,user,password)}
func(d *Desktop)Logout()error{return d.controller.Logout()}
func(d *Desktop)Connect()error{return d.controller.Connect()}
func(d *Desktop)Disconnect()error{return d.controller.Disconnect()}
func(d *Desktop)Refresh()error{return d.controller.Refresh()}
func(d *Desktop)Select(id string)error{return d.controller.Select(id)}
func(d *Desktop)SetMode(mode string,tun bool)error{return d.controller.SetMode(mode,tun)}

func main(){
	exe,e:=os.Executable();if e!=nil{log.Fatal(e)};base:=filepath.Dir(exe);dir:=filepath.Join(os.Getenv("LOCALAPPDATA"),"HQIP","Client")
	platform,e:=winclient.New(dir,filepath.Join(base,"sing-box.exe"));if e!=nil{log.Fatal(e)}
	core:=&client.Core{Path:filepath.Join(base,"sing-box.exe"),Dir:filepath.Join(dir,"runtime"),Platform:platform}
	c,e:=client.New(dir,platform,core);if e!=nil{log.Fatal(e)};defer c.Close();ctx,cancel:=context.WithCancel(context.Background());defer cancel();go c.Run(ctx)
	d:=&Desktop{controller:c};files,_:=fs.Sub(assets,"frontend/dist");var window *application.WebviewWindow
	app:=application.New(application.Options{Name:"HQIP",Description:"HQIP 私人网络",Services:[]application.Service{application.NewService(d)},Assets:application.AssetOptions{Handler:application.BundledAssetFileServer(files)},OnShutdown:func(){cancel();c.Close()},SingleInstance:&application.SingleInstanceOptions{UniqueID:"online.sharevps.hqip.client",OnSecondInstanceLaunch:func(application.SecondInstanceData){if window!=nil{window.Show();window.Focus()}}}})
	window=app.Window.NewWithOptions(application.WebviewWindowOptions{Title:"HQIP · 私人网络",Width:1020,Height:760,MinWidth:760,MinHeight:640,URL:"/",BackgroundColour:application.NewRGB(12,17,26)})
	window.RegisterHook(events.Common.WindowClosing,func(event *application.WindowEvent){window.Hide();event.Cancel()})
	tray:=app.SystemTray.New();tray.SetLabel("HQIP");menu:=app.NewMenu();menu.Add("打开 HQIP").OnClick(func(*application.Context){window.Show();window.Focus()});menu.Add("连接").OnClick(func(*application.Context){_ = c.Connect()});menu.Add("断开").OnClick(func(*application.Context){_ = c.Disconnect()});menu.AddSeparator();menu.Add("退出并恢复网络").OnClick(func(*application.Context){app.Quit()});tray.SetMenu(menu);tray.OnClick(func(){window.Show();window.Focus()})
	if e=app.Run();e!=nil{log.Print("HQIP 窗口退出: ",e)}
}
