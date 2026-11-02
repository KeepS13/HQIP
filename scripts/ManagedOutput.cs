using System;
using System.Collections.Concurrent;
using System.Diagnostics;
using System.IO;
using System.Threading.Tasks;

namespace HQIP {
    // Read both pipes continuously. Rotation owns the file handle, so Windows
    // never has to rename a file still held open by Start-Process redirection.
    public static class ManagedOutput {
        private static readonly ConcurrentDictionary<Process, Task> Readers = new ConcurrentDictionary<Process, Task>();

        public static Process Start(string executable, string arguments, string stdout, string stderr, long limit) {
            if (limit < 4096) throw new ArgumentOutOfRangeException("limit");
            var process = new Process();
            process.StartInfo = new ProcessStartInfo(executable, arguments) {
                UseShellExecute = false, CreateNoWindow = true,
                RedirectStandardOutput = true, RedirectStandardError = true
            };
            if (!process.Start()) throw new InvalidOperationException("Unable to start managed child");
            Stream output = process.StandardOutput.BaseStream;
            Stream error = process.StandardError.BaseStream;
            Task done = Task.WhenAll(Task.Run(() => Pump(output, stdout, limit)), Task.Run(() => Pump(error, stderr, limit)));
            Readers[process] = done;
            done.ContinueWith(task => { Task ignored; Readers.TryRemove(process, out ignored); });
            return process;
        }

        public static bool WaitForOutput(Process process, int milliseconds) {
            Task pending;
            return !Readers.TryGetValue(process, out pending) || pending.Wait(milliseconds);
        }

        private static void Rotate(string path, long limit) {
            string previous = path + ".1";
            // An older supervisor may have left an oversized log. Preserve its
            // final bounded portion rather than keeping an unbounded archive.
            using (var input = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite))
            using (var output = new FileStream(previous, FileMode.Create, FileAccess.Write, FileShare.Read)) {
                input.Seek(Math.Max(0, input.Length - limit), SeekOrigin.Begin);
                input.CopyTo(output, 4096);
            }
            using (var empty = new FileStream(path, FileMode.Create, FileAccess.Write, FileShare.Read)) { }
        }

        private static void Pump(Stream input, string path, long limit) {
            FileStream output = null;
            byte[] buffer = new byte[4096];
            try {
                int count;
                while ((count = input.Read(buffer, 0, buffer.Length)) != 0) {
                    try {
                        if (output == null) {
                            if (File.Exists(path) && new FileInfo(path).Length >= limit) Rotate(path, limit);
                            output = new FileStream(path, FileMode.Append, FileAccess.Write, FileShare.Read);
                        }
                        int offset = 0;
                        while (offset < count) {
                            if (output.Length >= limit) {
                                output.Dispose(); output = null;
                                Rotate(path, limit);
                                output = new FileStream(path, FileMode.Append, FileAccess.Write, FileShare.Read);
                            }
                            int write = (int)Math.Min(count - offset, limit - output.Length);
                            output.Write(buffer, offset, write);
                            offset += write;
                        }
                        output.Flush();
                    } catch (IOException) {
                        if (output != null) { output.Dispose(); output = null; }
                        // Disk errors must not fill a child process's stdout
                        // pipe and freeze the controller or SSH keepalives.
                    } catch (UnauthorizedAccessException) {
                        if (output != null) { output.Dispose(); output = null; }
                    }
                }
            } catch (IOException) { }
            finally {
                if (output != null) output.Dispose();
                input.Dispose();
            }
        }
    }
}
